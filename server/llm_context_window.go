package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/TaxMachine/ARTEX/db"
)

// Remote context-window resolution for profiles without a configured
// context_window_k. Only endpoints that actually publish per-model context
// limits are queried: OpenAI-compatible gateways such as vLLM/LiteLLM expose
// length/context_length on /v1/models, and Ollama exposes context_length in
// /api/tags model_info. Gateways that only list model ids — like the local
// IKHDev gateway — return nothing, and the window stays honestly "unknown"
// rather than guessed. No static model-family table: a wrong number labelled as
// fact is worse than "unknown".
//
// Results are cached per (profile, model) for 10 minutes: the resolution
// endpoint is polled every 10s and must not turn into a request storm.

const contextWindowCacheTTL = 10 * time.Minute

type contextWindowEntry struct {
	windowK int
	source  string // "remote" | "unknown"
	expires time.Time
}

var (
	contextWindowMu    sync.Mutex
	contextWindowCache = map[string]contextWindowEntry{}
)

// resolveContextWindowK returns the model's context window in K tokens and
// where it came from: "profile" (user-configured), "remote" (gateway-published)
// or "unknown". Zero window means unknown; callers must not substitute a
// default silently.
func (s *Server) resolveContextWindowK(ctx context.Context, p *db.LLMProfile) (int, string) {
	if p == nil {
		return 0, "unknown"
	}
	if p.ContextWindowK > 0 {
		return p.ContextWindowK, "profile"
	}
	key := fmt.Sprintf("p%d:%s", p.ID, p.Model)
	contextWindowMu.Lock()
	hit, ok := contextWindowCache[key]
	contextWindowMu.Unlock()
	if ok && time.Now().Before(hit.expires) {
		return hit.windowK, hit.source
	}
	windowK, source := 0, "unknown"
	switch strings.TrimSpace(p.Format) {
	case "openai", "": // OpenAI-compatible custom gateways, vLLM, LiteLLM…
		if w := s.openAIModelsWindow(ctx, p); w > 0 {
			windowK, source = w, "remote"
		}
	case "ollama":
		if w := s.ollamaNativeWindow(ctx, p); w > 0 {
			windowK, source = w, "remote"
		} else if w := s.openAIModelsWindow(ctx, p); w > 0 {
			windowK, source = w, "remote"
		}
	default: // anthropic / openai-responses list models without window fields
	}
	contextWindowMu.Lock()
	contextWindowCache[key] = contextWindowEntry{windowK: windowK, source: source, expires: time.Now().Add(contextWindowCacheTTL)}
	contextWindowMu.Unlock()
	return windowK, source
}

// openAIModelsWindow reads GET {base}/v1/models and pulls the matching model's
// context limit. Field names vary by gateway: vLLM exposes "length", LiteLLM
// "context_length", some "max_model_len"/"max_input_tokens". Best-effort with a
// short timeout; any failure yields 0 (unknown).
func (s *Server) openAIModelsWindow(ctx context.Context, p *db.LLMProfile) int {
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if base == "" {
		return 0
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	q, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(q, http.MethodGet, base+"/models", nil)
	if err != nil {
		return 0
	}
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	resp, err := sHTTPClientForProfile(p).Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Data) == 0 {
		var arr []map[string]any
		if json.Unmarshal(body, &arr) != nil {
			return 0
		}
		payload.Data = arr
	}
	model := strings.TrimSpace(p.Model)
	for _, m := range payload.Data {
		id, _ := m["id"].(string)
		name, _ := m["name"].(string)
		if id != model && name != model {
			continue
		}
		for _, field := range []string{"context_length", "length", "max_model_len", "max_input_tokens"} {
			if v, ok := numberField(m, field); ok && v > 0 {
				return int(v) / 1000
			}
		}
	}
	return 0
}

// ollamaNativeWindow reads GET {base}/api/tags → model_info["<model>.context_length"].
func (s *Server) ollamaNativeWindow(ctx context.Context, p *db.LLMProfile) int {
	base := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(p.BaseURL), "/"), "/v1")
	if base == "" {
		return 0
	}
	q, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(q, http.MethodGet, base+"/api/tags", nil)
	if err != nil {
		return 0
	}
	resp, err := sHTTPClientForProfile(p).Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0
	}
	var payload struct {
		Models []struct {
			Name      string         `json:"name"`
			Model     string         `json:"model"`
			ModelInfo map[string]any `json:"model_info"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return 0
	}
	model := strings.TrimSpace(p.Model)
	for _, m := range payload.Models {
		if m.Name != model && m.Model != model {
			continue
		}
		for k, raw := range m.ModelInfo {
			if !strings.HasSuffix(k, ".context_length") {
				continue
			}
			if n, ok := raw.(float64); ok && n > 0 {
				return int(n) / 1000
			}
		}
	}
	return 0
}

func numberField(m map[string]any, field string) (int64, bool) {
	for k, v := range m {
		if !strings.EqualFold(k, field) {
			continue
		}
		switch x := v.(type) {
		case float64:
			return int64(x), true
		case json.Number:
			if n, err := x.Int64(); err == nil {
				return n, true
			}
		case string:
			var n int64
			if _, err := fmt.Sscanf(strings.TrimSpace(x), "%d", &n); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

// sHTTPClientForProfile mirrors the chat client's proxy and user-agent choices
// for the few control requests made here. The session header is deliberately
// NOT sent: on chat requests it carries the session id as its value (the chat
// client reads it from the request context) and a one-off model listing has
// no session — so there is no legitimate value to put in it.
func sHTTPClientForProfile(p *db.LLMProfile) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	var transport http.RoundTripper = base
	if raw := strings.TrimSpace(p.Proxy); raw != "" {
		if u, err := url.Parse(raw); err == nil {
			switch u.Scheme {
			case "http", "https", "socks5":
				base.Proxy = http.ProxyURL(u)
			default:
				base.Proxy = nil
			}
		}
	} else {
		base.Proxy = nil
	}
	if ua := strings.TrimSpace(p.UserAgent); ua != "" {
		transport = &userAgentTransport{base: base, userAgent: ua}
	}
	return &http.Client{Timeout: 8 * time.Second, Transport: transport}
}

// userAgentTransport overrides the User-Agent on every request when configured.
type userAgentTransport struct {
	base      http.RoundTripper
	userAgent string
}

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", t.userAgent)
	}
	return t.base.RoundTrip(req)
}
