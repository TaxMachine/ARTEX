package server

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/TaxMachine/ARTEX/db"
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

// consoleOpen opens (or returns the existing) desktop session for operators:
// VNC when configured, else RDP when enabled on the machine (password
// credential required — resolved from machine-level creds, never task ones).
func (s *Server) consoleOpen(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	m, err := s.m.Machines().Machine(name)
	if err != nil {
		writeErr(w, 404, "machine not found")
		return
	}
	opt := s.m.Machines().DesktopOptionsFor(m)
	if opt.Port == 0 && m.Host != "" {
		// No VNC: try RDP (office-vm style headless Windows boxes).
		rdpUp, why := s.rdpReachable(r.Context(), m)
		if !rdpUp {
			writeErr(w, 409, "no VNC configured and RDP unavailable: "+why)
			return
		}
		user, pass, ok := s.machinePasswordCredential(m)
		if !ok {
			writeErr(w, 409, "RDP is on but no machine-level password credential is stored (add one on the machine form; RDP cannot use keys)")
			return
		}
		opt.Kind = "rdp"
		opt.Host = m.Host
		opt.Port = 3389
		opt.Username = user
		opt.Password = pass
	}
	info, err := s.m.Machines().DesktopOpen(machine.WithOperatorDial(r.Context()), m, opt)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, info)
}

// rdpReachable checks 3389 from the server and the machine-side enablement via
// one cheap SSH probe (operator scope).
func (s *Server) rdpReachable(ctx context.Context, m db.Machine) (bool, string) {
	if !machine.TCPReachable(m.Host, 3389, 2*time.Second) {
		return false, "port 3389 not reachable (enable RDP first — Lab state tab)"
	}
	sctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	out, err := s.m.Machines().Exec(sctx, 0, m.Name, "", 0,
		"powershell -NoProfile -EncodedCommand "+psEncode64Server(
			`Write-Output ((Get-ItemProperty 'HKLM:\System\CurrentControlSet\Control\Terminal Server' -Name fDenyTSConnections -ErrorAction SilentlyContinue).fDenyTSConnections)`),
		10*time.Second)
	if err != nil {
		return false, "probe failed: " + err.Error()
	}
	if strings.TrimSpace(out.Stdout) == "1" {
		return false, "RDP is disabled on the machine (fDenyTSConnections=1)"
	}
	return true, ""
}

// machinePasswordCredential finds a machine-level password credential.
func (s *Server) machinePasswordCredential(m db.Machine) (user, pass string, ok bool) {
	raw, has := m.Config["ssh_credentials"].([]any)
	if !has {
		return "", "", false
	}
	for _, e := range raw {
		c, isMap := e.(map[string]any)
		if !isMap {
			continue
		}
		st, _ := c["secret_type"].(string)
		p, _ := c["password"].(string)
		u, _ := c["username"].(string)
		if p != "" && (st == "password" || st == "") && u != "" {
			return u, p, true
		}
	}
	return "", "", false
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
		`$r += 'rdp_service=' + (Get-Service TermService -ErrorAction SilentlyContinue).Status`,
		`$r += 'rdp_enabled=' + $((Get-ItemProperty 'HKLM:\System\CurrentControlSet\Control\Terminal Server' -Name fDenyTSConnections -ErrorAction SilentlyContinue).fDenyTSConnections -eq 0)`,
		`$r += 'rdp_listening=' + @(Get-NetTCPConnection -LocalPort 3389 -State Listen -ErrorAction SilentlyContinue).Count`,
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

// machineRDPToggle enables or disables Remote Desktop on the machine over the
// operator SSH path (registry + firewall group + service). Idempotent.
func (s *Server) machineRDPToggle(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&body); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	m, err := s.m.Machines().Machine(name)
	if err != nil {
		writeErr(w, 404, "machine not found")
		return
	}
	deny := 1
	fw := "Disable-NetFirewallRule"
	if body.Enabled {
		deny = 0
		fw = "Enable-NetFirewallRule"
	}
	ps := strings.Join([]string{
		fmt.Sprintf(`Set-ItemProperty 'HKLM:\System\CurrentControlSet\Control\Terminal Server' -Name fDenyTSConnections -Value %d -Type DWord`, deny),
		fmt.Sprintf(`%s -DisplayGroup "Remote Desktop" -ErrorAction SilentlyContinue`, fw),
		`if ($deny -eq 0 -or $true) { }`,
		`Write-Output ("fDeny=" + (Get-ItemProperty 'HKLM:\System\CurrentControlSet\Control\Terminal Server' -Name fDenyTSConnections).fDenyTSConnections)`,
	}, "; ")
	ctx, cancel := context.WithTimeout(machine.WithOperatorDial(r.Context()), 30*time.Second)
	defer cancel()
	out, execErr := s.m.Machines().Exec(ctx, 0, m.Name, "", 0,
		"powershell -NoProfile -EncodedCommand "+psEncode64Server(ps), 25*time.Second)
	res := map[string]any{"machine": m.Name, "requested": body.Enabled}
	if execErr != nil {
		res["ok"] = false
		res["error"] = execErr.Error()
	} else {
		res["ok"] = strings.Contains(out.Stdout, fmt.Sprintf("fDeny=%d", deny))
		res["output"] = strings.TrimSpace(out.Stdout)
	}
	writeJSON(w, 200, res)
}

// machineOccupancy lists recent tasks that used the machine (from machine
// events) with their current status, plus the credential summary — the
// "everything about this machine" companion to the detail page.
func (s *Server) machineOccupancy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	m, err := s.m.Machines().Machine(name)
	if err != nil {
		writeErr(w, 404, "machine not found")
		return
	}
	out := map[string]any{"machine": m.Name}
	// recent tasks from the event stream
	rows, err := s.m.Machines().DB().Query(`
		SELECT e.task_id, COALESCE(t.name,''), COALESCE(t.status,'')
		FROM machine_events e LEFT JOIN tasks t ON t.id = e.task_id
		WHERE e.machine = $1 AND e.task_id IS NOT NULL
		GROUP BY e.task_id, t.name, t.status
		ORDER BY max(e.id) DESC LIMIT 8`, m.Name)
	if err == nil {
		defer rows.Close()
		var tasks []map[string]any
		for rows.Next() {
			var id int64
			var taskName, status sql.NullString
			if rows.Scan(&id, &taskName, &status) == nil {
				tasks = append(tasks, map[string]any{
					"task_id": id, "name": taskName.String, "status": status.String,
				})
			}
		}
		out["tasks"] = tasks
	}
	// credential summary (never the secrets)
	summary := map[string]any{"count": 0, "has_password": false, "default_username": ""}
	if raw, ok := m.Config["ssh_credentials"].([]any); ok {
		summary["count"] = len(raw)
		for _, e := range raw {
			if c, ok := e.(map[string]any); ok {
				if st, _ := c["secret_type"].(string); st == "password" {
					summary["has_password"] = true
				}
			}
		}
	}
	if u, ok := m.Config["default_username"].(string); ok {
		summary["default_username"] = u
	}
	out["credentials"] = summary
	writeJSON(w, 200, out)
}
