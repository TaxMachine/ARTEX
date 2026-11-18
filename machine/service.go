// Package machine implements ARTEX's native machine access: allowlisted VM
// lifecycle (libvirt or operator scripts), SSH exec/transfer, machine-credential
// resolution and evidence recording.
//
// Every operation starts from a row in the machines table. Agent input only ever
// names a machine; hosts, domains, scripts and ports come from the database.
package machine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

// ConvIDFromContext is re-exported so the machine layer can read a retest
// conversation from the call context without importing intercept at call sites.
var ConvIDFromContext = intercept.ConvIDFromContext

// protectedDomains are never mutated by ARTEX, even if someone registers them:
// the operator's primary VM must not be disturbed by an agent.
var protectedDomains = map[string]bool{
	"win11-26h2": true,
}

// Service is the machine backend shared by the agent tools and the HTTP API.
type Service struct {
	pg      *db.DB
	dataDir string
}

// New builds a Service. dataDir receives downloaded pcaps (data/machine_captures).
func New(pg *db.DB, dataDir string) *Service {
	return &Service{pg: pg, dataDir: strings.TrimRight(dataDir, "/")}
}

// defaultSvc is the process-wide backend set once at server startup. ToolSets
// created afterwards pick it up automatically; an explicit SetMachines still wins.
var defaultSvc *Service

// SetDefault installs the process-wide machine backend.
func SetDefault(s *Service) { defaultSvc = s }

// Default returns the process-wide backend (nil before startup/tests).
func Default() *Service { return defaultSvc }

// DB exposes the store for the server's listing/UI handlers.
func (s *Service) DB() *db.DB { return s.pg }

// List returns the registered machines.
func (s *Service) List() ([]db.Machine, error) { return s.pg.Machines().List() }

// Machine resolves one registered machine by name.
func (s *Service) Machine(name string) (db.Machine, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return db.Machine{}, errors.New("machine is required")
	}
	m, err := s.pg.Machines().Get(name)
	if err != nil {
		return db.Machine{}, fmt.Errorf("unknown machine %q (register it in Machines first)", name)
	}
	return m, nil
}

func (s *Service) guardMutation(m db.Machine) error {
	if m.Protected {
		return fmt.Errorf("machine %s is marked protected; lifecycle changes are refused", m.Name)
	}
	if protectedDomains[strings.ToLower(m.Name)] || protectedDomains[strings.ToLower(m.Domain)] {
		return fmt.Errorf("machine %s is on the protected list; lifecycle changes are refused", m.Name)
	}
	return nil
}

// TaskScopeOf resolves the task a machine operation belongs to. A task-bound
// call uses taskID directly; a task-less caller (retest conversation, chat)
// that carries a conversation id inherits the retest's origin task, so SSH
// credentials and machine evidence attribute to the finding's task.
func (s *Service) TaskScopeOf(ctx context.Context, taskID int64) int64 {
	if taskID > 0 {
		return taskID
	}
	conv := ConvIDFromContext(ctx)
	if conv == 0 {
		return 0
	}
	id, err := s.pg.RetestOriginTask(context.Background(), conv)
	if err != nil {
		return 0
	}
	return id
}

// Credential resolves the SSH credential for a machine and task. An explicit id
// wins; otherwise the newest task credential with protocol=ssh whose domain is
// the machine host or name (and username, when supplied).
func (s *Service) Credential(taskID int64, m db.Machine, username string, credentialID int64) (db.TaskCredential, error) {
	if taskID <= 0 {
		return db.TaskCredential{}, errors.New("machine tools need a running task")
	}
	if credentialID > 0 {
		c, err := s.pg.Credential(taskID, credentialID)
		if err != nil {
			return c, fmt.Errorf("credential %d not found in this task", credentialID)
		}
		if c.Protocol != "ssh" {
			return c, fmt.Errorf("credential %d is protocol %s, not ssh", c.ID, c.Protocol)
		}
		return c, nil
	}
	list, err := s.pg.TaskCredentials(taskID)
	if err != nil {
		return db.TaskCredential{}, err
	}
	// The operator can pin the default account per machine (lab VMs use the
	// unprivileged lpeuser for LPE work; admin actions pass username explicitly).
	if strings.TrimSpace(username) == "" {
		if v, ok := m.Config["default_username"].(string); ok {
			username = v
		}
	}
	host := strings.ToLower(strings.TrimSpace(m.Host))
	name := strings.ToLower(strings.TrimSpace(m.Name))
	var best *db.TaskCredential
	for i := range list {
		c := &list[i]
		if c.Protocol != "ssh" {
			continue
		}
		d := strings.ToLower(strings.TrimSpace(c.Domain))
		if d != host && d != name {
			continue
		}
		if username = strings.TrimSpace(username); username != "" && !strings.EqualFold(c.Username, username) {
			continue
		}
		if best == nil || c.ID > best.ID {
			best = c
		}
	}
	if best == nil {
		return db.TaskCredential{}, fmt.Errorf(
			"no ssh credential for machine %s (host %s). Ask the operator, or save one with "+
				"save_task_credential(protocol=\"ssh\", domain=\"%s\", username=\"...\", ...)", m.Name, m.Host, m.Host)
	}
	return *best, nil
}

// sshTarget resolves host/port/user/secret for an SSH call.
func (s *Service) sshTarget(taskID int64, machineName, username string, credentialID int64) (db.Machine, db.TaskCredential, error) {
	m, err := s.Machine(machineName)
	if err != nil {
		return m, db.TaskCredential{}, err
	}
	if strings.TrimSpace(m.Host) == "" {
		return m, db.TaskCredential{}, fmt.Errorf("machine %s has no host address", m.Name)
	}
	c, err := s.Credential(taskID, m, username, credentialID)
	if err != nil {
		return m, db.TaskCredential{}, err
	}
	return m, c, nil
}

// link records the task->machine dependency used by the startup boot sequence.
func (s *Service) link(taskID int64, m db.Machine) {
	if taskID > 0 {
		_ = s.pg.Machines().LinkTaskMachine(taskID, m.ID)
	}
}

// record writes one machine event, capping stored text so a huge transfer cannot
// bloat the database. The tool result is capped separately.
func (s *Service) record(e db.MachineEvent) (int64, error) {
	e.Stdout = truncate(e.Stdout, storedOutputCap)
	e.Stderr = truncate(e.Stderr, storedOutputCap)
	e.Command = truncate(e.Command, 16*1024)
	return s.pg.Machines().RecordEvent(e)
}

const (
	storedOutputCap = 128 * 1024
	returnOutputCap = 32 * 1024
)

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n...[truncated %d bytes]", len(s)-n)
}

func nowPtr(t time.Time) *time.Time { return &t }
