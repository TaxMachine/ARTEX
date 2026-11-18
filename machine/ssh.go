package machine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// ExecResult is the outcome of one SSH command.
type ExecResult struct {
	EventID  int64  `json:"event_id"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out,omitempty"`
	Duration int64  `json:"duration_ms"`
}

// TransferResult is the outcome of an upload/download.
type TransferResult struct {
	EventID   int64  `json:"event_id"`
	LocalPath string `json:"local_path,omitempty"`
	Remote    string `json:"remote_path"`
	Bytes     int64  `json:"bytes"`
	Duration  int64  `json:"duration_ms"`
}

func (s *Service) dialSSH(ctx context.Context, m db.Machine, c db.TaskCredential, timeout time.Duration) (*ssh.Client, error) {
	var auth []ssh.AuthMethod
	switch {
	case c.SecretType == "key":
		if strings.TrimSpace(c.PrivateKey) == "" {
			return nil, errors.New("credential is a key type but has no private key stored")
		}
		signer, err := ssh.ParsePrivateKey([]byte(c.PrivateKey))
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w (encrypted keys are not supported; use a password or an unencrypted key)", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	case c.Password != "":
		auth = append(auth, ssh.Password(c.Password))
	default:
		return nil, errors.New("credential has neither a password nor a private key")
	}
	port := m.SSHPort
	if port == 0 {
		port = 22
	}
	cfg := &ssh.ClientConfig{
		User: c.Username,
		Auth: auth,
		// Lab targets are disposable and often reinstalled; host keys change.
		// The credential is scoped to one task and one registered machine.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         timeout,
	}
	addr := net.JoinHostPort(m.Host, strconv.Itoa(port))
	// Target gate: dial through the pinned dialer so an approved pin is enforced
	// on the address actually opened (same contract as WebFetch/HTTP clients).
	// Ungated scopes (gate off, no scope) dial directly.
	conn, err := intercept.PinnedDialer("ssh", true)(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return ssh.NewClient(sshConn, chans, reqs), nil
}

// capBuffer keeps at most n bytes and tracks truncation.
type capBuffer struct {
	b     bytes.Buffer
	n     int
	trunc bool
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if c.b.Len() < c.n {
		remain := c.n - c.b.Len()
		if len(p) <= remain {
			c.b.Write(p)
		} else {
			c.b.Write(p[:remain])
			c.trunc = true
		}
	} else {
		c.trunc = true
	}
	return len(p), nil
}

func (c *capBuffer) String() string {
	out := c.b.String()
	if c.trunc {
		out += "\n...[truncated]"
	}
	return out
}

// Exec runs one command on a registered machine over SSH and records it as a
// machine event. A non-zero remote exit code is a normal result, not an error.
func (s *Service) Exec(ctx context.Context, taskID int64, machineName, username string, credentialID int64,
	command string, timeout time.Duration) (ExecResult, error) {
	if strings.TrimSpace(command) == "" {
		return ExecResult{}, errors.New("command is required")
	}
	if timeout <= 0 || timeout > 30*time.Minute {
		timeout = 2 * time.Minute
	}
	m, c, err := s.sshTarget(taskID, machineName, username, credentialID)
	if err != nil {
		return ExecResult{}, err
	}
	s.link(taskID, m)
	client, err := s.dialSSH(ctx, m, c, 20*time.Second)
	if err != nil {
		return ExecResult{}, fmt.Errorf("ssh %s@%s:%d: %w", c.Username, m.Host, m.SSHPort, err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return ExecResult{}, fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()
	stdout := &capBuffer{n: returnOutputCap}
	stderr := &capBuffer{n: 16 * 1024}
	session.Stdout, session.Stderr = stdout, stderr

	started := time.Now()
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	var runErr error
	var timedOut bool
	select {
	case runErr = <-done:
	case <-ctx.Done():
		timedOut = true
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		runErr = ctx.Err()
	case <-time.After(timeout):
		timedOut = true
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		runErr = fmt.Errorf("command timed out after %s", timeout)
	}
	dur := time.Since(started)

	exit := 0
	if runErr != nil {
		var exitErr *ssh.ExitError
		if errors.As(runErr, &exitErr) {
			exit = exitErr.ExitStatus()
			runErr = nil
		}
	}
	res := ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exit,
		TimedOut: timedOut,
		Duration: dur.Milliseconds(),
	}
	credID := c.ID
	res.EventID, _ = s.record(db.MachineEvent{
		TaskID:       &taskID,
		MachineID:    &m.ID,
		Machine:      m.Name,
		Protocol:     "ssh",
		Action:       "exec",
		Username:     c.Username,
		CredentialID: &credID,
		Command:      command,
		Stdout:       res.Stdout,
		Stderr:       res.Stderr,
		ExitCode:     &exit,
		DurationMS:   int(dur.Milliseconds()),
		Bytes:        int64(len(res.Stdout) + len(res.Stderr)),
		Meta:         map[string]any{"host": m.Host, "port": m.SSHPort, "timed_out": timedOut},
	})
	if runErr != nil && !timedOut {
		return res, runErr
	}
	if timedOut {
		return res, fmt.Errorf("command timed out after %s", timeout)
	}
	return res, nil
}

const maxTransferBytes = 256 << 20 // 256 MiB per file

// Upload copies a local file to the machine over SFTP.
func (s *Service) Upload(ctx context.Context, taskID int64, machineName, username string, credentialID int64,
	localPath, remotePath string) (TransferResult, error) {
	localPath = strings.TrimSpace(localPath)
	remotePath = strings.TrimSpace(remotePath)
	if localPath == "" || remotePath == "" {
		return TransferResult{}, errors.New("local_path and remote_path are required")
	}
	info, err := os.Stat(localPath)
	if err != nil {
		return TransferResult{}, fmt.Errorf("local file: %w", err)
	}
	if info.IsDir() {
		return TransferResult{}, errors.New("local_path is a directory; upload one file at a time")
	}
	if info.Size() > maxTransferBytes {
		return TransferResult{}, fmt.Errorf("file is %d bytes; the per-file cap is %d", info.Size(), maxTransferBytes)
	}
	m, c, err := s.sshTarget(taskID, machineName, username, credentialID)
	if err != nil {
		return TransferResult{}, err
	}
	s.link(taskID, m)
	client, err := s.dialSSH(ctx, m, c, 20*time.Second)
	if err != nil {
		return TransferResult{}, fmt.Errorf("ssh %s@%s:%d: %w", c.Username, m.Host, m.SSHPort, err)
	}
	defer client.Close()
	sc, err := sftp.NewClient(client)
	if err != nil {
		return TransferResult{}, fmt.Errorf("sftp: %w", err)
	}
	defer sc.Close()
	started := time.Now()
	src, err := os.Open(localPath)
	if err != nil {
		return TransferResult{}, err
	}
	defer src.Close()
	dst, err := sc.Create(remotePath)
	if err != nil {
		return TransferResult{}, fmt.Errorf("remote create %s: %w", remotePath, err)
	}
	n, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	dur := time.Since(started)
	credID := c.ID
	res := TransferResult{Remote: remotePath, Bytes: n, Duration: dur.Milliseconds()}
	res.EventID, _ = s.record(db.MachineEvent{
		TaskID: &taskID, MachineID: &m.ID, Machine: m.Name, Protocol: "ssh", Action: "upload",
		Username: c.Username, CredentialID: &credID, Command: localPath + " -> " + remotePath,
		DurationMS: int(dur.Milliseconds()), Bytes: n,
		Meta: map[string]any{"host": m.Host, "port": m.SSHPort, "local": localPath},
	})
	if copyErr != nil {
		return res, fmt.Errorf("upload: %w", copyErr)
	}
	return res, nil
}

// Download copies a remote file to a local path (default: data/machine_transfers).
func (s *Service) Download(ctx context.Context, taskID int64, machineName, username string, credentialID int64,
	remotePath, localPath string) (TransferResult, error) {
	remotePath = strings.TrimSpace(remotePath)
	if remotePath == "" {
		return TransferResult{}, errors.New("remote_path is required")
	}
	if strings.TrimSpace(localPath) == "" {
		localPath = filepath.Join(s.dataDir, "machine_transfers", machineName, filepath.Base(remotePath))
	}
	localPath = filepath.Clean(localPath)
	if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
		return TransferResult{}, err
	}
	m, c, err := s.sshTarget(taskID, machineName, username, credentialID)
	if err != nil {
		return TransferResult{}, err
	}
	s.link(taskID, m)
	client, err := s.dialSSH(ctx, m, c, 20*time.Second)
	if err != nil {
		return TransferResult{}, fmt.Errorf("ssh %s@%s:%d: %w", c.Username, m.Host, m.SSHPort, err)
	}
	defer client.Close()
	sc, err := sftp.NewClient(client)
	if err != nil {
		return TransferResult{}, fmt.Errorf("sftp: %w", err)
	}
	defer sc.Close()
	src, err := sc.Open(remotePath)
	if err != nil {
		return TransferResult{}, fmt.Errorf("remote open %s: %w", remotePath, err)
	}
	defer src.Close()
	started := time.Now()
	dst, err := os.Create(localPath)
	if err != nil {
		return TransferResult{}, err
	}
	n, copyErr := io.Copy(dst, io.LimitReader(src, maxTransferBytes+1))
	closeErr := dst.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if n > maxTransferBytes {
		_ = os.Remove(localPath)
		return TransferResult{}, fmt.Errorf("remote file exceeds the %d byte cap", maxTransferBytes)
	}
	dur := time.Since(started)
	credID := c.ID
	res := TransferResult{LocalPath: localPath, Remote: remotePath, Bytes: n, Duration: dur.Milliseconds()}
	res.EventID, _ = s.record(db.MachineEvent{
		TaskID: &taskID, MachineID: &m.ID, Machine: m.Name, Protocol: "ssh", Action: "download",
		Username: c.Username, CredentialID: &credID, Command: remotePath + " -> " + localPath,
		DurationMS: int(dur.Milliseconds()), Bytes: n,
		Meta: map[string]any{"host": m.Host, "port": m.SSHPort, "local": localPath},
	})
	if copyErr != nil {
		return res, fmt.Errorf("download: %w", copyErr)
	}
	return res, nil
}
