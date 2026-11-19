package machine

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/TaxMachine/ARTEX/db"
)

// ShutdownAll gracefully stops the running registered machines before the
// container exits: libvirt `shutdown`, or ACPI `system_powerdown` on a script
// VM's QEMU monitor (config.monitor_port), waiting for the process to exit. A
// machine that does not stop in time falls back to its registered stop script.
//
// Budget is the total wall clock; call it from the shutdown path only.
func (s *Service) ShutdownAll(ctx context.Context, budget time.Duration, logf func(string, ...any)) {
	if logf == nil {
		logf = log.Printf
	}
	list, err := s.List()
	if err != nil {
		logf("[machines] shutdown: list: %v", err)
		return
	}
	deadline := time.Now().Add(budget)
	for _, m := range list {
		if time.Now().After(deadline) || ctx.Err() != nil {
			logf("[machines] shutdown: budget exhausted; remaining machines left running")
			return
		}
		if m.Protected {
			continue
		}
		st, err := s.Status(ctx, m.Name)
		if err != nil || !st.Running {
			continue
		}
		started := time.Now()
		logf("[machines] shutdown %s (%s)", m.Name, m.Kind)
		switch m.Kind {
		case "libvirt":
			_, _ = s.virsh(ctx, "shutdown", m.Domain)
			s.waitStopped(ctx, m, 20*time.Second)
		case "script":
			if port := configInt(m, "monitor_port", 0); port > 0 {
				if err := acpiPowerdown(port); err != nil {
					logf("[machines] shutdown %s: monitor: %v", m.Name, err)
				} else if s.waitStopped(ctx, m, 30*time.Second) {
					break
				}
			}
			// Not stopped gracefully: fall back to the operator's stop script.
			if _, err := s.runScript(ctx, m.StopScript, m.Workdir, 60*time.Second); err != nil {
				logf("[machines] shutdown %s: stop script: %v", m.Name, err)
			}
		}
		exit := 0
		detail := "stopped"
		if st2, _ := s.Status(ctx, m.Name); st2.Running {
			exit, detail = 1, "still running"
		}
		_, _ = s.record(db.MachineEvent{
			MachineID: &m.ID, Machine: m.Name, Protocol: "vm", Action: "shutdown",
			Command: detail, ExitCode: &exit, DurationMS: int(time.Since(started).Milliseconds()),
		})
	}
}

func (s *Service) waitStopped(ctx context.Context, m db.Machine, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if m.Kind == "script" {
			if !scriptAlive(m, scriptPID(m)) {
				return true
			}
		} else if st, err := s.Status(ctx, m.Name); err == nil && !st.Running {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
	}
}

// acpiPowerdown sends the HMP `system_powerdown` command to a QEMU monitor.
func acpiPowerdown(port int) error {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 3*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	_, err = conn.Write([]byte("system_powerdown\n"))
	return err
}
