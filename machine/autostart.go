package machine

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/TaxMachine/ARTEX/db"
)

// BootResult is one machine handled by the startup sequence.
type BootResult struct {
	Machine    string `json:"machine"`
	Kind       string `json:"kind"`
	Action     string `json:"action"` // started|already-running|skipped|failed
	State      string `json:"state"`
	PID        int    `json:"pid,omitempty"`
	SSHReady   bool   `json:"ssh_ready"`
	DurationMS int64  `json:"duration_ms"`
	Reason     string `json:"reason,omitempty"`
	Error      string `json:"error,omitempty"`
}

func memAvailableMB() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemAvailable:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				kb, _ := strconv.ParseInt(f[1], 10, 64)
				return kb / 1024
			}
		}
	}
	return 0
}

func configInt(m db.Machine, key string, def int) int {
	switch v := m.Config[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// Autostart boots the machines needed by active tasks. taskID>0 restricts it to
// one task (manual "boot this task's machines", including paused tasks).
//
// Only linked machines are considered (task_machines), and a machine can opt out
// with config.autostart=false. Boots are sequential and memory-guarded, so a
// reboot cannot start more VMs than the host can hold.
func (s *Service) Autostart(ctx context.Context, taskID int64, logf func(string, ...any)) ([]BootResult, error) {
	if logf == nil {
		logf = log.Printf
	}
	var (
		targets []db.Machine
		err     error
	)
	if taskID > 0 {
		targets, err = s.pg.Machines().TaskMachines(taskID)
	} else {
		targets, err = s.pg.Machines().AutostartCandidates()
	}
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		logf("[machines] autostart: no task machines to boot")
		return nil, nil
	}
	logf("[machines] autostart: %d machine(s) for %s", len(targets), map[bool]string{true: fmt.Sprintf("task %d", taskID), false: "active tasks"}[taskID > 0])

	results := make([]BootResult, 0, len(targets))
	for _, m := range targets {
		started := time.Now()
		res := BootResult{Machine: m.Name, Kind: m.Kind}
		fail := func(format string, args ...any) {
			res.Action = "failed"
			res.Error = fmt.Sprintf(format, args...)
			res.DurationMS = time.Since(started).Milliseconds()
			logf("[machines] autostart %s: %s", m.Name, res.Error)
			s.recordAutostart(m, res)
			results = append(results, res)
		}

		if m.Protected {
			res.Action, res.Reason = "skipped", "protected"
			res.DurationMS = time.Since(started).Milliseconds()
			logf("[machines] autostart %s: skipped (protected)", m.Name)
			s.recordAutostart(m, res)
			results = append(results, res)
			continue
		}
		if v, ok := m.Config["autostart"].(bool); ok && !v {
			res.Action, res.Reason = "skipped", "config.autostart=false"
			res.DurationMS = time.Since(started).Milliseconds()
			s.recordAutostart(m, res)
			results = append(results, res)
			continue
		}

		st, err := s.Status(ctx, m.Name)
		if err != nil {
			fail("status: %v", err)
			continue
		}
		res.State, res.PID = st.State, st.PID
		timeout := time.Duration(configInt(m, "boot_timeout_seconds", 180)) * time.Second
		if st.Running {
			if st.SSHReachable {
				res.Action, res.SSHReady, res.Reason = "already-running", true, "VM and SSH already up"
				res.DurationMS = time.Since(started).Milliseconds()
				s.recordAutostart(m, res)
				results = append(results, res)
				continue
			}
			// Already running but the guest is still booting: wait rather than
			// issuing a second start.
			res.Action = "already-running"
			ready, werr := s.waitSSH(ctx, m, timeout)
			res.SSHReady = ready
			if !ready {
				res.Error = fmt.Sprintf("running but SSH not ready within %s: %v", timeout, werr)
			}
			res.DurationMS = time.Since(started).Milliseconds()
			s.recordAutostart(m, res)
			results = append(results, res)
			continue
		}
		if m.Kind == "ssh" {
			res.Action, res.Reason = "skipped", "ssh-only machine has no lifecycle"
			res.DurationMS = time.Since(started).Milliseconds()
			s.recordAutostart(m, res)
			results = append(results, res)
			continue
		}

		// Memory guard: never start a VM that does not fit in MemAvailable.
		need := configInt(m, "memory_mb", 4096)
		reserve := configInt(m, "reserve_mb", 2048)
		if avail := memAvailableMB(); avail > 0 && int64(need+reserve) > avail {
			res.Action, res.Reason = "skipped", fmt.Sprintf("insufficient memory: %d MB available, need %d MB + %d MB reserve", avail, need, reserve)
			res.DurationMS = time.Since(started).Milliseconds()
			logf("[machines] autostart %s: %s", m.Name, res.Reason)
			s.recordAutostart(m, res)
			results = append(results, res)
			continue
		}

		logf("[machines] autostart %s: starting (%s)", m.Name, m.Kind)
		if _, err := s.Start(ctx, 0, m.Name); err != nil {
			fail("start: %v", err)
			continue
		}
		ready, werr := s.waitSSH(ctx, m, timeout)
		res.Action, res.SSHReady = "started", ready
		if !ready {
			res.Error = fmt.Sprintf("SSH not ready within %s: %v", timeout, werr)
		}
		st, _ = s.Status(ctx, m.Name)
		res.State, res.PID = st.State, st.PID
		res.DurationMS = time.Since(started).Milliseconds()
		logf("[machines] autostart %s: %s ssh=%v in %dms", m.Name, res.Action, res.SSHReady, res.DurationMS)
		s.recordAutostart(m, res)
		results = append(results, res)
	}
	return results, nil
}

func (s *Service) waitSSH(ctx context.Context, m db.Machine, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		if tcpReachable(m.Host, m.SSHPort, 2*time.Second) {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, fmt.Errorf("timeout after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(4 * time.Second):
		}
	}
}

func (s *Service) recordAutostart(m db.Machine, r BootResult) {
	exit := 0
	if r.Action == "failed" || r.Error != "" {
		exit = 1
	}
	_, _ = s.record(db.MachineEvent{
		MachineID: &m.ID, Machine: m.Name, Protocol: "vm", Action: "autostart",
		Command: r.Action, Stdout: r.Reason, Stderr: r.Error, ExitCode: &exit,
		DurationMS: int(r.DurationMS),
		Meta:       map[string]any{"state": r.State, "ssh_ready": r.SSHReady, "pid": r.PID},
	})
}
