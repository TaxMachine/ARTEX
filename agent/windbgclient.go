package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// windbg HTTP MCP client: the same server behind the mcp__windbg__ tools
// (bridge on 172.17.0.1:13012), driven directly from crash_triage_auto.
// Config: ARTEX_WINDBG_URL (http://172.17.0.1:13012/mcp) + ARTEX_WINDBG_TOKEN.

type windbgClient struct {
	base  string
	token string
	http  *http.Client
}

func newWinDbgClient() *windbgClient {
	base := strings.TrimSpace(os.Getenv("ARTEX_WINDBG_URL"))
	token := strings.TrimSpace(os.Getenv("ARTEX_WINDBG_TOKEN"))
	if base == "" || token == "" {
		return nil
	}
	return &windbgClient{base: base, token: token, http: &http.Client{Timeout: 300 * time.Second}}
}

func (c *windbgClient) post(ctx context.Context, body map[string]any, sid string, out any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	// SSE frames: find the data: line carrying our id
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var parsed json.RawMessage
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "data: ") {
			var probe struct {
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(line[6:]), &probe) == nil {
				if probe.Error != nil {
					return fmt.Errorf("mcp error: %s", probe.Error.Message)
				}
				if probe.Result != nil {
					parsed = probe.Result
				}
			}
		}
	}
	if parsed == nil {
		return fmt.Errorf("no result in response")
	}
	if out != nil {
		return json.Unmarshal(parsed, out)
	}
	return nil
}

func (c *windbgClient) initialize(ctx context.Context) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base, strings.NewReader(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"artex-triage","version":"1"}}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	sid := resp.Header.Get("Mcp-Session-Id")
	if sid == "" {
		return "", fmt.Errorf("no session id from initialize")
	}
	// initialized notification (best effort)
	nr, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base, strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	nr.Header.Set("Content-Type", "application/json")
	nr.Header.Set("Mcp-Session-Id", sid)
	nr.Header.Set("Authorization", "Bearer "+c.token)
	_, _ = c.http.Do(nr)
	return sid, nil
}

func (c *windbgClient) call(ctx context.Context, sid, tool string, args map[string]any, timeout time.Duration) (map[string]any, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	callBody := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}}
	if err := c.post(cctx, callBody, sid, &out); err != nil {
		return nil, err
	}
	var texts []string
	for _, c := range out.Content {
		texts = append(texts, c.Text)
	}
	res := map[string]any{"text": strings.Join(texts, "\n")}
	if out.IsError {
		res["is_error"] = true
	}
	// try to parse the text as JSON (some tools return structured text)
	var nested map[string]any
	if len(texts) > 0 && json.Unmarshal([]byte(texts[0]), &nested) == nil {
		for k, v := range nested {
			res[k] = v
		}
	}
	return res, nil
}

func (c *windbgClient) endSession(ctx context.Context, sid string) {
	_, _ = c.call(ctx, sid, "end_session", map[string]any{}, 30*time.Second)
}

// moduleOffsetRE pulls "MODULE+0xOFFSET" tokens from triage text.
var moduleOffsetRE = regexp.MustCompile(`([A-Za-z0-9_\-]+!?[A-Za-z0-9_]+)?\+?(0x[0-9a-fA-F]{4,})`)

// crossmapSymbolize maps the faulting module+offset from triage output to a
// crossmap function when the module is indexed. The crossmap proxy is reached
// through the agent's own http() helper when wired; here we use the env URL.
func crossmapSymbolize(triage map[string]any) map[string]any {
	text := fmt.Sprint(triage["text"])
	// prefer explicit faulting-module lines
	faultRE := regexp.MustCompile(`(?i)faulting[^:\n]*:\s*([A-Za-z0-9_.\-]+)(?:\+|\s+)(0x[0-9a-fA-F]+)`)
	m := faultRE.FindStringSubmatch(text)
	if m == nil {
		// fall back to first module+offset token
		mm := moduleOffsetRE.FindStringSubmatch(text)
		if mm == nil {
			return nil
		}
		m = mm
	}
	out := map[string]any{"module": m[1], "offset": m[2]}
	// crossmap lookup is best-effort: the agent has the mcp__crossmap__ tools;
	// leave the refs for the agent to resolve with map_search.
	out["hint"] = fmt.Sprintf("resolve %s functions around %s with map_search/map_node (crossmap)", m[1], m[2])
	return out
}
