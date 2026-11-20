package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/TaxMachine/ARTEX/db"
	actool "github.com/TaxMachine/ARTEX/third_party/norma/tool"
)

// Native machine tools: registered VM lifecycle, SSH exec/transfer, machine
// evidence and pcap captures. All of them go through ToolSet.machines, which the
// server installs per task; they never accept a host, domain or script path from
// the model.

func (t *ToolSet) needMachines(name string, run func(context.Context, json.RawMessage) (actool.Result, error)) func(context.Context, json.RawMessage) (actool.Result, error) {
	return func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		if t.machines == nil {
			return actool.Errorf(name + " is not available: the native machine backend is not initialized on this server. Register machines and restart ARTEX with the libvirt/SSH mounts."), nil
		}
		return run(ctx, in)
	}
}

func (t *ToolSet) listMachines() actool.CoreTool {
	return readTool("list_machines",
		"List the operator-registered machines (VMs/SSH targets) available to this lab, with kind, host, SSH port and notes. Only machines registered by the operator can be used; names here are the only valid `machine` inputs elsewhere.",
		obj(map[string]any{}),
		t.needMachines("list_machines", func(ctx context.Context, _ json.RawMessage) (actool.Result, error) {
			list, err := t.machines.List()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{"machines": list})
		}))
}

func (t *ToolSet) machineStatus() actool.CoreTool {
	return readTool("machine_status",
		"Observe one registered machine: VM running state / QEMU pid (script VMs), libvirt domain state, and whether its SSH port is reachable. Read-only; does not record evidence.",
		obj(map[string]any{"machine": str("registered machine name from list_machines")}, "machine"),
		t.needMachines("machine_status", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ Machine string }
			_ = json.Unmarshal(in, &a)
			st, err := t.machines.Status(ctx, a.Machine)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(st)
		}))
}

func (t *ToolSet) machineStart() actool.CoreTool {
	return writeTool("machine_start",
		"Boot one registered machine (libvirt `start`, or the operator's registered start script for raw-QEMU lab VMs). Waits for the start call to return, not for the guest OS; poll machine_status / ssh_exec after. Refuses protected machines.",
		obj(map[string]any{"machine": str("registered machine name")}, "machine"),
		t.needMachines("machine_start", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ Machine string }
			_ = json.Unmarshal(in, &a)
			st, err := t.machines.Start(ctx, t.taskID, a.Machine)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(st)
		}))
}

func (t *ToolSet) machineStop() actool.CoreTool {
	return writeTool("machine_stop",
		"Shut one registered machine down. force=false uses libvirt shutdown (ACPI) or the registered stop script; force=true uses libvirt destroy. The VM's disk overlay is kept. Refuses protected machines.",
		obj(map[string]any{
			"machine": str("registered machine name"),
			"force":   map[string]any{"type": "boolean", "description": "power off immediately instead of a graceful shutdown (default false)"},
		}, "machine"),
		t.needMachines("machine_stop", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine string
				Force   bool
			}
			_ = json.Unmarshal(in, &a)
			st, err := t.machines.Stop(ctx, t.taskID, a.Machine, a.Force)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(st)
		}))
}

func (t *ToolSet) machineReset() actool.CoreTool {
	return writeTool("machine_reset",
		"Hard-restart one registered machine (libvirt `reset`, or registered stop+start for script VMs). State in the guest is lost; disks are kept. Refuses protected machines.",
		obj(map[string]any{"machine": str("registered machine name")}, "machine"),
		t.needMachines("machine_reset", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ Machine string }
			_ = json.Unmarshal(in, &a)
			st, err := t.machines.Reset(ctx, t.taskID, a.Machine)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(st)
		}))
}

func (t *ToolSet) machineSnapshot() actool.CoreTool {
	return writeTool("machine_snapshot",
		"Manage libvirt snapshots of a registered machine: action=list|create|revert. `snapshot` names the snapshot for create/revert. Script-backed lab VMs do not support snapshots.",
		obj(map[string]any{
			"machine":  str("registered libvirt machine name"),
			"action":   map[string]any{"type": "string", "enum": []string{"list", "create", "revert"}, "description": "snapshot operation"},
			"snapshot": str("snapshot name (create/revert); 1-64 chars, letters/digits/._-"),
		}, "machine", "action"),
		t.needMachines("machine_snapshot", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ Machine, Action, Snapshot string }
			_ = json.Unmarshal(in, &a)
			out, err := t.machines.Snapshot(ctx, t.taskID, a.Machine, a.Action, a.Snapshot)
			if err != nil {
				return actool.Errorf(err.Error() + "\n" + out), nil
			}
			return jsonResult(map[string]any{"machine": a.Machine, "action": a.Action, "output": out})
		}))
}

func (t *ToolSet) sshExec() actool.CoreTool {
	return writeTool("ssh_exec",
		"Run a command on a registered machine over native SSH and record it as machine evidence. Two command modes — use ps= for PowerShell targets, it is CHEAPER AND SAFER than hand-writing prefixes: pass plain PowerShell source in ps (no 'powershell -Command', no escaping — the server transports it as an -EncodedCommand, immune to the cmd.exe/quote mangling that breaks hand-written prefixes) or a VM-side .ps1 path in ps_file (adds -ExecutionPolicy Bypass; runs scripts the policy blocks otherwise). Raw shell commands (dir, whoami, tar) still use command=. With the task's target gate on, the machine's SSH endpoint must be pinned first. The credential resolves from this task's machine credentials (protocol=ssh, domain=machine host/name); pass credential_id or username to disambiguate. Non-zero exit codes are returned normally, not as tool errors.",
		obj(map[string]any{
			"machine":         str("registered machine name"),
			"command":         str("raw shell command (cmd.exe on Windows, sh on Linux) — prefer ps= for PowerShell"),
			"ps":              str("PowerShell source to run (server wraps it in -EncodedCommand: no prefix, no quote escaping needed)"),
			"ps_file":         str("VM path of a .ps1 to execute (-ExecutionPolicy Bypass -File <path>); for scripts already uploaded"),
			"ps_args":         str("optional arguments appended after ps_file"),
			"username":        str("optional SSH username to select among stored credentials"),
			"credential_id":   idp("optional task credential id to use (must be protocol=ssh)"),
			"timeout_seconds": intp("command timeout, default 120, max 1800"),
		}, "machine"),
		t.needMachines("ssh_exec", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine      string `json:"machine"`
				Command      string `json:"command"`
				Ps           string `json:"ps"`
				PsFile       string `json:"ps_file"`
				PsArgs       string `json:"ps_args"`
				Username     string `json:"username"`
				CredentialID int64  `json:"credential_id"`
				Timeout      int    `json:"timeout_seconds"`
			}
			_ = json.Unmarshal(in, &a)
			cmd := a.Command
			switch {
			case a.Ps != "":
				// Plain PowerShell source -> -EncodedCommand (UTF-16LE base64):
				// the agent writes NO prefix and NO cmd.exe escaping; the
				// transport survives the quote mangling that breaks
				// hand-written 'powershell -Command "..."'.
				cmd = "powershell -NoProfile -EncodedCommand " + psEncode64(a.Ps)
			case a.PsFile != "":
				safe := strings.TrimSpace(a.PsFile)
				if !safeVMPath(safe) || !strings.HasSuffix(strings.ToLower(safe), ".ps1") {
					return actool.Errorf("ps_file must be an absolute VM path to a .ps1"), nil
				}
				cmd = fmt.Sprintf("powershell -NoProfile -ExecutionPolicy Bypass -File %s", safe)
				if args := strings.TrimSpace(a.PsArgs); args != "" {
					if strings.ContainsAny(args, "\"&|><`") {
						return actool.Errorf("ps_args must not contain shell metacharacters"), nil
					}
					cmd += " " + args
				}
			}
			if strings.TrimSpace(cmd) == "" {
				return actool.Errorf("one of command/ps/ps_file is required"), nil
			}
			to := time.Duration(a.Timeout) * time.Second
			if a.Timeout <= 0 {
				to = 2 * time.Minute
			}
			res, err := t.machines.Exec(ctx, t.taskID, a.Machine, a.Username, a.CredentialID, cmd, to)
			if err != nil {
				return jsonResult(map[string]any{"error": err.Error(), "result": res})
			}
			// Return the capped output directly; the full text is in the event.
			return jsonResult(res)
		}))
}

func (t *ToolSet) sshUpload() actool.CoreTool {
	return writeTool("ssh_upload",
		"Copy one local file to a registered machine over SFTP and record the transfer as machine evidence. Target-gated like ssh_exec: the machine's SSH endpoint needs a pin. Per-file cap 256 MiB. Use for dropping a PoC/harness on the target.",
		obj(map[string]any{
			"machine":       str("registered machine name"),
			"local_path":    str("path in this task's workspace/container"),
			"remote_path":   str("destination path on the target"),
			"username":      str("optional SSH username"),
			"credential_id": idp("optional task credential id"),
		}, "machine", "local_path", "remote_path"),
		t.needMachines("ssh_upload", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine, LocalPath, RemotePath, Username string
				CredentialID                             int64 `json:"credential_id"`
			}
			_ = json.Unmarshal(in, &a)
			res, err := t.machines.Upload(ctx, t.taskID, a.Machine, a.Username, a.CredentialID, a.LocalPath, a.RemotePath)
			if err != nil {
				return jsonResult(map[string]any{"error": err.Error(), "result": res})
			}
			return jsonResult(res)
		}))
}

func (t *ToolSet) sshDownload() actool.CoreTool {
	return writeTool("ssh_download",
		"Copy one remote file from a registered machine over SFTP and record it as machine evidence. Target-gated like ssh_exec: the machine's SSH endpoint needs a pin. Defaults to data/machine_transfers/<machine>/<name>. Per-file cap 256 MiB.",
		obj(map[string]any{
			"machine":       str("registered machine name"),
			"remote_path":   str("file path on the target"),
			"local_path":    str("optional destination path; default data/machine_transfers/<machine>/<name>"),
			"username":      str("optional SSH username"),
			"credential_id": idp("optional task credential id"),
		}, "machine", "remote_path"),
		t.needMachines("ssh_download", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine, RemotePath, LocalPath, Username string
				CredentialID                             int64 `json:"credential_id"`
			}
			_ = json.Unmarshal(in, &a)
			res, err := t.machines.Download(ctx, t.taskID, a.Machine, a.Username, a.CredentialID, a.RemotePath, a.LocalPath)
			if err != nil {
				return jsonResult(map[string]any{"error": err.Error(), "result": res})
			}
			return jsonResult(res)
		}))
}

func (t *ToolSet) machineEvents() actool.CoreTool {
	return readTool("machine_events",
		"Search this task's machine evidence: one row per SSH command/transfer or VM action, with action, command, exit code, duration and a short output preview. Use it to review what already ran on a machine before running it again. Get the full stdout/stderr with machine_event_get.",
		obj(map[string]any{
			"machine": str("optional machine name filter"),
			"action":  str("optional action filter: exec|upload|download|start|stop|reset|snapshot_create|capture_start|capture_stop"),
			"query":   str("optional substring over command/stdout/stderr"),
			"limit":   intp("page size, default 20, max 200"),
			"offset":  intp("offset for pagination"),
		}),
		t.needMachines("machine_events", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if t.taskID <= 0 {
				return actool.Errorf("machine_events requires a task context"), nil
			}
			var a struct {
				Machine, Action, Query string
				Limit, Offset          int
			}
			_ = json.Unmarshal(in, &a)
			list, err := t.machines.DB().Machines().Events(db.MachineEventFilter{
				TaskID: t.taskID, Machine: a.Machine, Action: a.Action, Query: a.Query,
				Limit: a.Limit, Offset: a.Offset,
			})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			// Preview only: full output is one call away.
			for i := range list {
				list[i].Stdout = preview(list[i].Stdout, 600)
				list[i].Stderr = preview(list[i].Stderr, 300)
			}
			return jsonResult(map[string]any{"events": list})
		}))
}

func (t *ToolSet) machineEventGet() actool.CoreTool {
	return readTool("machine_event_get",
		"Fetch one machine event in full by id (command, stdout, stderr, exit code, duration, metadata). Scoped to this task.",
		obj(map[string]any{"id": idp("event id from machine_events")}, "id"),
		t.needMachines("machine_event_get", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			if t.taskID <= 0 {
				return actool.Errorf("machine_event_get requires a task context"), nil
			}
			var a struct {
				ID int64 `json:"id"`
			}
			_ = json.Unmarshal(in, &a)
			e, err := t.machines.DB().Machines().Event(a.ID, t.taskID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(e)
		}))
}

func (t *ToolSet) captureStart() actool.CoreTool {
	return writeTool("capture_start",
		"Start a packet capture on a registered machine over SSH — the TRAFFIC-PROOF tool: wrap exploit/payload/callback traffic (RCE proofs, msf handler sessions, malicious document fetches) in a capture and cite the pcap in the finding's evidence. Linux machines use tcpdump (needs it installed; config.capture_sudo or a root credential for privileges); Windows machines use the built-in pktmon (no install, captures to ETL, auto-converted to pcapng on stop). Works whether or not the machine's traffic goes through the HTTP capture proxy — machine capture sees ALL protocols (SMB, DNS, TCP callbacks), not just HTTP. The capture stays on the guest until capture_stop, which downloads it into data/machine_captures/<machine>/ and records it as machine evidence. iface defaults to any; bpf_filter is a tcpdump expression on Linux / a simplified host-port filter on Windows (empty = all traffic).",
		obj(map[string]any{
			"machine":       str("registered machine name"),
			"iface":         str("interface, default any"),
			"bpf_filter":    str("tcpdump filter, e.g. 'tcp port 445' (Linux); simplified host/port filter on Windows"),
			"username":      str("optional SSH username"),
			"credential_id": idp("optional task credential id"),
		}, "machine"),
		t.needMachines("capture_start", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine      string `json:"machine"`
				Iface        string `json:"iface"`
				BPF          string `json:"bpf_filter"`
				Username     string `json:"username"`
				CredentialID int64  `json:"credential_id"`
			}
			_ = json.Unmarshal(in, &a)
			cap, err := t.machines.CaptureStart(ctx, t.taskID, a.Machine, a.Iface, a.BPF, a.Username, a.CredentialID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(cap)
		}))
}

func (t *ToolSet) captureStop() actool.CoreTool {
	return writeTool("capture_stop",
		"Stop one packet capture, download the pcap/pcapng into data/machine_captures/<machine>/<id>.pcap and record the result as machine evidence (Windows pktmon captures are converted from ETL to pcapng first). Returns the local path — cite it in the finding's evidence: handler transcript + pcap is the complete RCE proof.",
		obj(map[string]any{
			"machine":       str("registered machine name"),
			"capture_id":    idp("capture id returned by capture_start"),
			"username":      str("optional SSH username"),
			"credential_id": idp("optional task credential id"),
		}, "machine", "capture_id"),
		t.needMachines("capture_stop", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine      string `json:"machine"`
				CaptureID    int64  `json:"capture_id"`
				Username     string `json:"username"`
				CredentialID int64  `json:"credential_id"`
			}
			_ = json.Unmarshal(in, &a)
			cap, err := t.machines.CaptureStop(ctx, t.taskID, a.Machine, a.CaptureID, a.Username, a.CredentialID)
			if err != nil {
				return jsonResult(map[string]any{"error": err.Error(), "capture": cap})
			}
			return jsonResult(cap)
		}))
}

func (t *ToolSet) captureList() actool.CoreTool {
	return readTool("capture_list",
		"List recent packet captures for a registered machine: id, interface, filter, status, bytes, local pcap path. Check this to cite an existing capture as evidence instead of re-capturing.",
		obj(map[string]any{
			"machine": str("registered machine name"),
			"limit":   intp("default 10, max 100"),
		}, "machine"),
		t.needMachines("capture_list", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine string `json:"machine"`
				Limit   int    `json:"limit"`
			}
			_ = json.Unmarshal(in, &a)
			list, err := t.machines.Captures(a.Machine, a.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{"captures": list})
		}))
}

func preview(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
