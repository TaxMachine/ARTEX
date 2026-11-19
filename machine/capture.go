package machine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/TaxMachine/ARTEX/db"
	"golang.org/x/crypto/ssh"
)

var safeIfaceRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,32}$`)
var safeBPFRE = regexp.MustCompile(`^[A-Za-z0-9_.:()!<>= /-]{0,256}$`)

// captureCommand runs a command for capture management without adding an
// "exec" event (the capture action itself is the event).
func (s *Service) captureCommand(ctx context.Context, m db.Machine, c db.TaskCredential, command string, timeout time.Duration) (string, string, int, error) {
	client, err := s.dialSSH(ctx, m, c, 20*time.Second)
	if err != nil {
		return "", "", -1, err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return "", "", -1, err
	}
	defer session.Close()
	stdout := &capBuffer{n: 64 * 1024}
	stderr := &capBuffer{n: 16 * 1024}
	session.Stdout, session.Stderr = stdout, stderr
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	var runErr error
	select {
	case runErr = <-done:
	case <-ctx.Done():
		_ = session.Close()
		return stdout.String(), stderr.String(), -1, ctx.Err()
	case <-time.After(timeout):
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		return stdout.String(), stderr.String(), -1, fmt.Errorf("capture command timed out after %s", timeout)
	}
	exit := 0
	if runErr != nil {
		var ee *ssh.ExitError
		if errors.As(runErr, &ee) {
			exit = ee.ExitStatus()
			runErr = nil
		}
	}
	return stdout.String(), stderr.String(), exit, runErr
}

func (s *Service) captureEvent(taskID int64, m db.Machine, action, command, stdout, stderr string, exit int, pcapPath string) int64 {
	var task *int64
	if taskID > 0 {
		task = &taskID
	}
	id, _ := s.record(db.MachineEvent{
		TaskID: task, MachineID: &m.ID, Machine: m.Name, Protocol: "pcap", Action: action,
		Command: command, Stdout: stdout, Stderr: stderr, ExitCode: &exit, PcapPath: pcapPath,
	})
	return id
}

// CaptureStart begins a tcpdump capture on a Linux machine over SSH.
func (s *Service) CaptureStart(ctx context.Context, taskID int64, name, iface, bpf string, username string, credentialID int64) (db.MachineCapture, error) {
	m, c, err := s.sshTarget(taskID, name, username, credentialID)
	if err != nil {
		return db.MachineCapture{}, err
	}
	s.link(taskID, m)
	iface = strings.TrimSpace(iface)
	if iface == "" {
		iface = "any"
	}
	if !safeIfaceRE.MatchString(iface) {
		return db.MachineCapture{}, errors.New("iface must be 1-32 chars: letters, digits, dot, dash, underscore, colon")
	}
	bpf = strings.TrimSpace(bpf)
	if !safeBPFRE.MatchString(bpf) {
		return db.MachineCapture{}, errors.New("bpf_filter contains unsupported characters")
	}
	stamp := time.Now().UTC().Format("20060102T150405")
	pcap := fmt.Sprintf("/tmp/artex-%s-%s.pcap", safeBase(m.Name), stamp)
	pidf := fmt.Sprintf("/tmp/artex-%s-%s.pid", safeBase(m.Name), stamp)
	logf := fmt.Sprintf("/tmp/artex-%s-%s.log", safeBase(m.Name), stamp)
	prefix := "tcpdump"
	if v, ok := m.Config["capture_sudo"].(bool); ok && v {
		prefix = "sudo -n tcpdump"
	}
	cmd := fmt.Sprintf(
		"nohup %s -i %s -U -w %s %s >%s 2>&1 & p=$!; echo $p; sleep 1; "+
			"if kill -0 $p 2>/dev/null; then echo RUNNING; else echo FAILED; cat %s; fi",
		prefix, iface, pcap, bpf, logf, logf)
	stdout, stderr, exit, runErr := s.captureCommand(ctx, m, c, cmd, 45*time.Second)
	if runErr != nil {
		s.captureEvent(taskID, m, "capture_start", cmd, stdout, stderr, exit, pcap)
		return db.MachineCapture{}, runErr
	}
	if !strings.Contains(stdout, "RUNNING") {
		msg := "tcpdump did not stay running\n" + stdout + stderr
		s.captureEvent(taskID, m, "capture_start", cmd, msg, stderr, 1, pcap)
		return db.MachineCapture{}, fmt.Errorf("capture failed to start (tcpdump installed? privileges?): %s", truncate(msg, 1200))
	}
	cap := db.MachineCapture{MachineID: m.ID, TaskID: &taskID, Iface: iface, BPFFilter: bpf, RemotePath: pcap, PIDFile: pidf}
	id, err := s.pg.Machines().CreateCapture(cap)
	if err != nil {
		return db.MachineCapture{}, err
	}
	cap.ID = id
	s.captureEvent(taskID, m, "capture_start", cmd, stdout, stderr, 0, pcap)
	return cap, nil
}

// CaptureStop stops a capture, downloads the pcap into data/machine_captures and
// records the result. The capture record is updated even when the download fails.
func (s *Service) CaptureStop(ctx context.Context, taskID int64, name string, captureID int64, username string, credentialID int64) (db.MachineCapture, error) {
	m, c, err := s.sshTarget(taskID, name, username, credentialID)
	if err != nil {
		return db.MachineCapture{}, err
	}
	s.link(taskID, m)
	cap, err := s.pg.Machines().Capture(captureID)
	if err != nil {
		return db.MachineCapture{}, fmt.Errorf("capture %d: %w", captureID, err)
	}
	if cap.MachineID != m.ID {
		return db.MachineCapture{}, fmt.Errorf("capture %d belongs to another machine", captureID)
	}
	if cap.Status != "running" {
		return cap, fmt.Errorf("capture %d is already %s", captureID, cap.Status)
	}
	// The pid file lives on the guest, not the host; read it remotely.
	cmd := fmt.Sprintf("p=$(cat %s 2>/dev/null); [ -n \"$p\" ] && kill -INT $p 2>/dev/null; sleep 1; ls -l %s 2>/dev/null || true",
		cap.PIDFile, cap.RemotePath)
	stdout, stderr, exit, runErr := s.captureCommand(ctx, m, c, cmd, 30*time.Second)
	if runErr != nil {
		s.captureEvent(taskID, m, "capture_stop", cmd, stdout, stderr, exit, cap.RemotePath)
		return cap, runErr
	}
	localDir := filepath.Join(s.dataDir, "machine_captures", safeBase(m.Name))
	local := filepath.Join(localDir, fmt.Sprintf("%d.pcap", cap.ID))
	dl, dlErr := s.Download(ctx, taskID, m.Name, username, credentialID, cap.RemotePath, local)
	if dlErr != nil {
		_ = s.pg.Machines().FinishCapture(cap.ID, "", 0, "failed")
		s.captureEvent(taskID, m, "capture_stop", cmd, stdout, stderr, 1, cap.RemotePath)
		return cap, fmt.Errorf("stop ok but download failed: %w", dlErr)
	}
	_ = s.pg.Machines().FinishCapture(cap.ID, dl.LocalPath, dl.Bytes, "stopped")
	s.captureEvent(taskID, m, "capture_stop", cmd, stdout, stderr, 0, dl.LocalPath)
	cap, _ = s.pg.Machines().Capture(cap.ID)
	return cap, nil
}

// Captures lists recent captures for a machine.
func (s *Service) Captures(name string, limit int) ([]db.MachineCapture, error) {
	m, err := s.Machine(name)
	if err != nil {
		return nil, err
	}
	return s.pg.Machines().Captures(m.ID, limit)
}

func safeBase(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return b.String()
}
