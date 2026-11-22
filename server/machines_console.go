package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/TaxMachine/ARTEX/machine"
)

// Machine management revamp — operator-facing endpoints:
//   - test-connection: verify a machine's SSH credential (machine-level or the
//     first matching task credential) with one cheap exec; returns whoami +
//     hostname + latency. Powers the Credentials tab and the wizard's verify
//     step, so a broken key is visible before a task stalls on it.
//   - console: operator VNC window. DesktopScreen/Input already existed for
//     agents; these expose open/screenshot/input/close at task scope 0 with the
//     machine-level credential, and serve the PNG bytes directly.
//   - lab-state: one batched SSH probe aggregating the lab health operators
//     keep checking by hand: page_heap IFEO flags, fuzzer processes, defender
//     exclusions, disk/memory/uptime. Host-side windbg bridge reachability is
//     checked from the server itself.

// machineTestConnection verifies SSH reachability + credential for a machine.
func (s *Server) machineTestConnection(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	m, err := s.m.Machines().Machine(name)
	if err != nil {
		writeErr(w, 404, "machine not found")
		return
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(machine.WithOperatorDial(r.Context()), 25*time.Second)
	defer cancel()
	// taskID 0: the machine-level credential path (config.ssh_credentials)
	out, execErr := s.m.Machines().Exec(ctx, 0, m.Name, r.URL.Query().Get("username"), 0,
		"whoami & hostname", 20*time.Second)
	res := map[string]any{
		"machine":     m.Name,
		"ok":          execErr == nil && out.ExitCode == 0,
		"latency_ms":  time.Since(started).Milliseconds(),
		"exit_code":   out.ExitCode,
		"ssh_present": m.SSHPort != 0,
	}
	if execErr != nil {
		res["error"] = execErr.Error()
	}
	if out.Stdout != "" {
		res["output"] = strings.TrimSpace(out.Stdout)
	}
	if out.Stderr != "" {
		res["stderr"] = strings.TrimSpace(out.Stderr)
	}
	_ = ctx
	writeJSON(w, 200, res)
}

// consoleOpen opens (or returns the existing) VNC desktop session for operators.
func (s *Server) consoleOpen(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	m, err := s.m.Machines().Machine(name)
	if err != nil {
		writeErr(w, 404, "machine not found")
		return
	}
	opt := s.m.Machines().DesktopOptionsFor(m)
	if opt.Port == 0 {
		writeErr(w, 409, "no console configured for this machine (set config.vnc_port)")
		return
	}
	info, err := s.m.Machines().DesktopOpen(r.Context(), m, opt)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, info)
}

// consoleScreen grabs a fresh screenshot and serves the PNG bytes.
func (s *Server) consoleScreen(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sess, png, w_, h_, err := s.m.Machines().ConsoleScreen(r.Context(), name)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Console-Size", fmt.Sprintf("%dx%d", w_, h_))
	w.Header().Set("X-Console-Session", sess)
	_, _ = w.Write(png)
}

// consoleInput sends one operator input event (click/type/key) to the console.
func (s *Server) consoleInput(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Action string `json:"action"` // click | type | key
		X      int    `json:"x"`
		Y      int    `json:"y"`
		Button string `json:"button"`
		Text   string `json:"text"`
		Keys   string `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var out map[string]any
	var err error
	switch body.Action {
	case "click":
		out, err = s.m.Machines().DesktopClick(ctx, 0, name, body.X, body.Y, body.Button)
	case "type":
		out, err = s.m.Machines().DesktopType(ctx, 0, name, body.Text)
	case "key":
		out, err = s.m.Machines().DesktopKey(ctx, 0, name, body.Keys)
	default:
		writeErr(w, 400, "action must be click|type|key")
		return
	}
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// consoleClose closes the operator console session.
func (s *Server) consoleClose(w http.ResponseWriter, r *http.Request) {
	out, err := s.m.Machines().DesktopClose(r.Context(), r.PathValue("name"))
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

// machineLabState aggregates lab health for a machine: one batched SSH probe
// (page_heap IFEO, fuzzer processes, defender exclusions, disk/memory/uptime)
// plus host-side windbg-bridge reachability. Windows-flavored; non-Windows
// machines get the ssh/basic subset.
func (s *Server) machineLabState(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	m, err := s.m.Machines().Machine(name)
	if err != nil {
		writeErr(w, 404, "machine not found")
		return
	}
	ctx, cancel := context.WithTimeout(machine.WithOperatorDial(r.Context()), 40*time.Second)
	defer cancel()

	out := map[string]any{"machine": m.Name, "os": m.OS}

	// Batched probe: every check in one PowerShell round trip. Each line is
	// key=value so the parse is trivial and robust.
	probe := strings.Join([]string{
		`$r=@()`,
		`$r += 'uptime_s=' + ([int]((Get-Date) - (Get-CimInstance Win32_OperatingSystem).LastBootUpTime).TotalSeconds)`,
		`$r += 'free_disk_mb=' + [int]((Get-PSDrive C).Free/1MB)`,
		`$r += 'free_mem_mb=' + [int]((Get-CimInstance Win32_OperatingSystem).FreePhysicalMemory/1KB)`,
		`foreach ($p in 'WINWORD','EXCEL') { $k="HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Image File Execution Options\$p.EXE"; $v=(Get-ItemProperty $k -ErrorAction SilentlyContinue); $r += "page_heap_${p}=" + $(if ($v.GlobalFlag) {'FULL flags=' + $v.GlobalFlag + '/' + $v.PageHeapFlags} else {'off'}) }`,
		`$r += 'fuzzer_procs=' + @(Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" | Where-Object {$_.CommandLine -match 'artex-fuzzer'}).Count`,
		`try { $r += 'defender_exclusions=' + @((Get-MpPreference -ErrorAction Stop).ExclusionPath).Count } catch { $r += 'defender_exclusions=unknown' }`,
		`$r += 'office_procs=' + @(Get-Process WINWORD,EXCEL -ErrorAction SilentlyContinue).Count`,
		`$r -join "|"`,
	}, "; ")
	if ex, execErr := s.m.Machines().Exec(ctx, 0, m.Name, "", 0,
		"powershell -NoProfile -EncodedCommand "+psEncode64Server(probe), 35*time.Second); execErr != nil {
		out["ssh_ok"] = false
		out["ssh_error"] = execErr.Error()
	} else {
		out["ssh_ok"] = true
		for _, line := range strings.FieldsFunc(ex.Stdout, func(r rune) bool { return r == '\n' || r == '|' }) {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
				out[k] = v
			}
		}
		if strings.Contains(strings.ToLower(m.OS), "windows") == false {
			// non-windows output parse: keep raw
			out["raw"] = strings.TrimSpace(ex.Stdout)
		}
	}

	// Host-side: the windbg MCP bridge the machine's tooling depends on.
	if addr := os.Getenv("ARTEX_WINDBG_URL"); addr != "" {
		client := &http.Client{Timeout: 4 * time.Second}
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, addr,
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"lab-state","version":"1"}}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if tok := os.Getenv("ARTEX_WINDBG_TOKEN"); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		if resp, err := client.Do(req); err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			out["windbg_bridge"] = resp.StatusCode == 200
		} else {
			out["windbg_bridge"] = false
		}
	}
	writeJSON(w, 200, out)
}

// psEncode64Server mirrors the agent-side UTF-16LE EncodedCommand transport.
func psEncode64Server(script string) string {
	u16 := make([]byte, 0, len(script)*2)
	for _, b := range []byte(script) {
		u16 = append(u16, b, 0)
	}
	return base64.StdEncoding.EncodeToString(u16)
}

// machineVerifyCredentials SSH-dials an arbitrary endpoint with explicit
// credentials (no machine row needed) — the registration wizard's verify step.
// Nothing is persisted; one whoami round trip.
func (s *Server) machineVerifyCredentials(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Host       string `json:"host"`
		Port       int    `json:"port"`
		Username   string `json:"username"`
		SecretType string `json:"secret_type"` // key | password
		PrivateKey string `json:"private_key"`
		Password   string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<18)).Decode(&body); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if body.Host == "" || body.Username == "" {
		writeErr(w, 400, "host and username are required")
		return
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(machine.WithOperatorDial(r.Context()), 25*time.Second)
	defer cancel()
	out, err := s.m.Machines().VerifySSH(ctx, machine.VerifyReq{
		Host: body.Host, Port: body.Port, Username: body.Username,
		SecretType: body.SecretType, PrivateKey: body.PrivateKey, Password: body.Password,
	})
	res := map[string]any{"ok": err == nil && out.ExitCode == 0, "latency_ms": time.Since(started).Milliseconds()}
	if err != nil {
		res["error"] = err.Error()
	} else {
		res["output"] = strings.TrimSpace(out.Stdout)
		if out.ExitCode != 0 {
			res["error"] = strings.TrimSpace(out.Stderr)
		}
	}
	writeJSON(w, 200, res)
}
