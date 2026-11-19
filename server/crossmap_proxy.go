package server

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// crossmapProxy forwards a subset of the crossmap function-graph API to the
// browser under the ARTEX origin, so the project canvas never makes a
// cross-origin call to the docker bridge. Only the read endpoints the UI needs
// are exposed; the registry is operator-owned (ARTEX_CROSSMAP_URL).
func (s *Server) crossmapProxy(w http.ResponseWriter, r *http.Request) {
	if s.crossmapURL == "" {
		writeErr(w, 503, "crossmap is not configured")
		return
	}
	rest := r.PathValue("rest")
	allowed := map[string]bool{
		"map/expand": true, "map/decompile": true, "map/paths": true,
		"map/search": true, "map/summary": true, "map/binaries": true,
		"map/stats": true, "map/node": true,
	}
	prefix := rest
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		prefix = rest[:i]
	}
	if !allowed[prefix] {
		writeErr(w, 404, "unknown crossmap route")
		return
	}
	target := s.crossmapURL + "/" + rest
	if rq := r.URL.RawQuery; rq != "" {
		target += "?" + rq
	}
	client := &http.Client{Timeout: 240 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		writeErr(w, 502, "crossmap unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

var _ = url.QueryEscape
