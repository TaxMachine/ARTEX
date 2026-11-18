package machine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/db"
)

// Status is one machine's observed state.
type Status struct {
	Machine      string `json:"machine"`
	Kind         string `json:"kind"`
	Running      bool   `json:"running"`
	State        string `json:"state"`
	Host         string `json:"host,omitempty"`
	SSHPort      int    `json:"ssh_port,omitempty"`
	SSHReachable bool   `json:"ssh_reachable"`
	PID          int    `json:"pid,omitempty"`
	Detail       string `json:"detail,omitempty"`
}

var safeDomainRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var safeSnapshotRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func (s *Service) virsh(ctx context.Context, args ...string) (string, error) {
	timeout := 30 * time.Second
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	argv := append([]string{"-c", "qemu:///system"}, args...)
	cmd := exec.CommandContext(cctx, "virsh", argv...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if cctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("virsh timed out after %s", timeout)
	}
	if err != nil {
		return text, fmt.Errorf("virsh %s: %w: %s", strings.Join(args, " "), err, text)
	}
	return text, nil
}

func (s *Service) runScript(ctx context.Context, path, workdir string, timeout time.Duration) (string, error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", errors.New("registered script path is empty or not absolute")
	}
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		return "", fmt.Errorf("registered script is missing: %s", path)
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, path)
	if workdir != "" {
		cmd.Dir = workdir
	}
	out, err := cmd.CombinedOutput()
	text := truncate(strings.TrimSpace(string(out)), 16*1024)
	if cctx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("script timed out after %s", timeout)
	}
	if err != nil {
		return text, fmt.Errorf("script %s: %w: %s", filepath.Base(path), err, text)
	}
	return text, nil
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 probes liveness without delivering anything.
	return p.Signal(syscall.Signal(0)) == nil
}

func scriptPID(m db.Machine) int {
	pidfile := ""
	if v, ok := m.Config["pidfile"].(string); ok {
		pidfile = v
	}
	if pidfile == "" && m.Workdir != "" {
		pidfile = filepath.Join(m.Workdir, "snapshot", "qemu-"+m.Name+".pid")
	}
	if pidfile == "" {
		return 0
	}
	b, err := os.ReadFile(pidfile)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

func tcpReachable(host string, port int, timeout time.Duration) bool {
	if host == "" {
		return false
	}
	if port == 0 {
		port = 22
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Status reports liveness without mutating anything (and without an event).
func (s *Service) Status(ctx context.Context, name string) (Status, error) {
	m, err := s.Machine(name)
	if err != nil {
		return Status{}, err
	}
	st := Status{Machine: m.Name, Kind: m.Kind, Host: m.Host, SSHPort: m.SSHPort}
	switch m.Kind {
	case "libvirt":
		if !safeDomainRE.MatchString(m.Domain) {
			return st, fmt.Errorf("registered libvirt domain %q is not a safe name", m.Domain)
		}
		out, err := s.virsh(ctx, "domstate", m.Domain)
		if err != nil {
			st.State = "unknown"
			st.Detail = out
		} else {
			st.State = strings.TrimSpace(strings.Split(out, "\n")[0])
			st.Running = !strings.Contains(strings.ToLower(st.State), "shut off") &&
				!strings.Contains(strings.ToLower(st.State), "crashed")
		}
	case "script":
		pid := scriptPID(m)
		st.PID = pid
		st.Running = pidAlive(pid)
		if st.Running {
			st.State = "running"
		} else {
			st.State = "stopped"
		}
	case "ssh":
		st.State = "unknown"
	}
	if m.Host != "" {
		st.SSHReachable = tcpReachable(m.Host, m.SSHPort, 2*time.Second)
	}
	return st, nil
}

func (s *Service) lifecycleEvent(taskID int64, m db.Machine, action, detail string, duration time.Duration, err error) {
	var task *int64
	if taskID > 0 {
		task = &taskID
	}
	exit := 0
	stderr := ""
	if err != nil {
		exit = 1
		stderr = err.Error()
	}
	_, _ = s.record(db.MachineEvent{
		TaskID: task, MachineID: &m.ID, Machine: m.Name, Protocol: "vm", Action: action,
		Command: detail, Stdout: detail, Stderr: stderr, ExitCode: &exit,
		DurationMS: int(duration.Milliseconds()),
	})
}

// Start boots a registered machine.
func (s *Service) Start(ctx context.Context, taskID int64, name string) (Status, error) {
	m, err := s.Machine(name)
	if err != nil {
		return Status{}, err
	}
	if err := s.guardMutation(m); err != nil {
		return Status{}, err
	}
	started := time.Now()
	var runErr error
	var detail string
	switch m.Kind {
	case "libvirt":
		if !safeDomainRE.MatchString(m.Domain) {
			return Status{}, fmt.Errorf("registered libvirt domain %q is not a safe name", m.Domain)
		}
		detail, runErr = s.virsh(ctx, "start", m.Domain)
	case "script":
		detail, runErr = s.runScript(ctx, m.StartScript, m.Workdir, 3*time.Minute)
	default:
		return Status{}, fmt.Errorf("machine %s has kind %s and no lifecycle", m.Name, m.Kind)
	}
	s.lifecycleEvent(taskID, m, "start", detail, time.Since(started), runErr)
	if runErr != nil {
		return Status{}, runErr
	}
	return s.Status(ctx, m.Name)
}

// Stop shuts a machine down. force=true destroys/power-offs instead of a
// graceful shutdown.
func (s *Service) Stop(ctx context.Context, taskID int64, name string, force bool) (Status, error) {
	m, err := s.Machine(name)
	if err != nil {
		return Status{}, err
	}
	if err := s.guardMutation(m); err != nil {
		return Status{}, err
	}
	started := time.Now()
	var runErr error
	var detail string
	switch m.Kind {
	case "libvirt":
		if !safeDomainRE.MatchString(m.Domain) {
			return Status{}, fmt.Errorf("registered libvirt domain %q is not a safe name", m.Domain)
		}
		verb := "shutdown"
		if force {
			verb = "destroy"
		}
		detail, runErr = s.virsh(ctx, verb, m.Domain)
	case "script":
		detail, runErr = s.runScript(ctx, m.StopScript, m.Workdir, 2*time.Minute)
	default:
		return Status{}, fmt.Errorf("machine %s has kind %s and no lifecycle", m.Name, m.Kind)
	}
	s.lifecycleEvent(taskID, m, "stop", detail, time.Since(started), runErr)
	if runErr != nil {
		return Status{}, runErr
	}
	return s.Status(ctx, m.Name)
}

// Reset is a hard restart: virsh reset for libvirt (running domains only) or
// stop+start for script machines.
func (s *Service) Reset(ctx context.Context, taskID int64, name string) (Status, error) {
	m, err := s.Machine(name)
	if err != nil {
		return Status{}, err
	}
	if err := s.guardMutation(m); err != nil {
		return Status{}, err
	}
	started := time.Now()
	var runErr error
	var detail string
	switch m.Kind {
	case "libvirt":
		if !safeDomainRE.MatchString(m.Domain) {
			return Status{}, fmt.Errorf("registered libvirt domain %q is not a safe name", m.Domain)
		}
		detail, runErr = s.virsh(ctx, "reset", m.Domain)
	case "script":
		var out1, out2 string
		out1, runErr = s.runScript(ctx, m.StopScript, m.Workdir, 2*time.Minute)
		if runErr == nil {
			out2, runErr = s.runScript(ctx, m.StartScript, m.Workdir, 3*time.Minute)
		}
		detail = strings.TrimSpace(out1 + "\n" + out2)
	default:
		return Status{}, fmt.Errorf("machine %s has kind %s and no lifecycle", m.Name, m.Kind)
	}
	s.lifecycleEvent(taskID, m, "reset", detail, time.Since(started), runErr)
	if runErr != nil {
		return Status{}, runErr
	}
	return s.Status(ctx, m.Name)
}

// Snapshot performs create|list|revert on a libvirt machine.
func (s *Service) Snapshot(ctx context.Context, taskID int64, name, action, snapshot string) (string, error) {
	m, err := s.Machine(name)
	if err != nil {
		return "", err
	}
	if m.Kind != "libvirt" {
		return "", fmt.Errorf("snapshots are only supported for libvirt machines (this is %s)", m.Kind)
	}
	if !safeDomainRE.MatchString(m.Domain) {
		return "", fmt.Errorf("registered libvirt domain %q is not a safe name", m.Domain)
	}
	started := time.Now()
	var out string
	switch action {
	case "list":
		out, err = s.virsh(ctx, "snapshot-list", m.Domain)
	case "create":
		if !safeSnapshotRE.MatchString(snapshot) {
			return "", errors.New("snapshot name must be 1-64 chars: letters, digits, dot, dash, underscore")
		}
		out, err = s.virsh(ctx, "snapshot-create-as", m.Domain, snapshot, "--description", "artex")
	case "revert":
		if !safeSnapshotRE.MatchString(snapshot) {
			return "", errors.New("snapshot name must be 1-64 chars: letters, digits, dot, dash, underscore")
		}
		out, err = s.virsh(ctx, "snapshot-revert", m.Domain, snapshot)
	default:
		return "", errors.New("action must be list|create|revert")
	}
	if action != "list" {
		s.lifecycleEvent(taskID, m, "snapshot_"+action, out, time.Since(started), err)
	}
	if err != nil {
		return out, err
	}
	return out, nil
}
