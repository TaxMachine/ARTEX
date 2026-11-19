package machine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/TaxMachine/ARTEX/db"
)

// startCaptureUnix runs tcpdump on a Linux/Unix guest. The pid is written to
// the pid file so CaptureStop can SIGINT exactly this capture.
func (s *Service) startCaptureUnix(ctx context.Context, taskID int64, m db.Machine, c db.TaskCredential, iface, bpf, pcap, pidf, logf string) (string, string, int, error) {
	prefix := "tcpdump"
	if v, ok := m.Config["capture_sudo"].(bool); ok && v {
		prefix = "sudo -n tcpdump"
	}
	cmd := fmt.Sprintf(
		"nohup %s -i %s -U -w %s %s >%s 2>&1 & p=$!; echo $p > %s; sleep 1; "+
			"if kill -0 $p 2>/dev/null; then echo RUNNING; else echo FAILED; cat %s; fi",
		prefix, iface, pcap, bpf, logf, pidf, logf)
	return s.captureCommand(ctx, m, c, cmd, 45*time.Second)
}

// startCaptureWindows runs pktmon (built into Windows 10 1709+ / 11) on a
// Windows guest. pktmon collects ETL, converted to pcapng after stop. iface is
// translated to an interface index via Get-NetAdapter when it is not a number.
// bpf is passed through as a pktmon -f filter when it is empty or a simple
// host/port expression; complex BPF programs are ignored (pktmon has its own
// filter syntax) with a note in the log.
func (s *Service) startCaptureWindows(ctx context.Context, taskID int64, m db.Machine, c db.TaskCredential, iface, bpf, etl, pidf, logf string) (string, string, int, error) {
	// Translate a named interface to its ifIndex; leave numbers/`any` as-is.
	ifIdx := iface
	if !isAllDigits(iface) && !strings.EqualFold(iface, "any") {
		ps := fmt.Sprintf(`powershell -NoProfile -Command "(Get-NetAdapter -Name '%s' -ErrorAction SilentlyContinue).ifIndex"`, sanitizeWinPSString(iface))
		out, _, _, err := s.captureCommand(ctx, m, c, ps, 20*time.Second)
		if err == nil {
			if v := strings.TrimSpace(out); isAllDigits(strings.SplitN(v, "\r\n", 2)[0]) {
				ifIdx = strings.SplitN(v, "\r\n", 2)[0]
			}
		}
		if !isAllDigits(ifIdx) {
			ifIdx = "" // capture all interfaces
		}
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`pktmon stop --name artex 2>nul & pktmon filter remove 2>nul & `))
	if bpf != "" {
		// pktmon -f accepts a simplified filter (host/port/IP); pass it through.
		sb.WriteString(fmt.Sprintf(`pktmon filter add -f "%s" 2>nul & `, sanitizeWinPSString(bpf)))
	}
	if ifIdx != "" && !strings.EqualFold(ifIdx, "any") {
		sb.WriteString(fmt.Sprintf(`pktmon start --name artex --etw -p 0 -c %s -f %s -m multi-file 2>&1`, ifIdx, etl))
	} else {
		sb.WriteString(fmt.Sprintf(`pktmon start --etw -p 0 -c comp -f %s -m multi-file 2>&1`, etl))
	}
	sb.WriteString(` & echo RUNNING`)
	return s.captureCommand(ctx, m, c, sb.String(), 45*time.Second)
}

// stopCaptureWindows stops pktmon and converts the ETL to pcapng next to it;
// the converted file becomes the download path.
func (s *Service) stopCaptureWindows(ctx context.Context, taskID int64, m db.Machine, c db.TaskCredential, cap db.MachineCapture) (remotePath string, stdout, stderr string, exit int, err error) {
	pcapng := strings.TrimSuffix(cap.RemotePath, filepath.Ext(cap.RemotePath)) + ".pcapng"
	cmd := fmt.Sprintf(`pktmon stop 2>&1 & pktmon etl2pcapng %s --out %s 2>&1 & dir /b %s 2>nul`, cap.RemotePath, pcapng, pcapng)
	stdout, stderr, exit, err = s.captureCommand(ctx, m, c, cmd, 60*time.Second)
	if err == nil && !strings.Contains(stdout, pcapng) && !strings.Contains(stderr, pcapng) {
		// etl2pcapng names output by ETL suffix when --out is unsupported (older builds)
		stdout += "\n[note] pcapng path: " + pcapng
	}
	return pcapng, stdout, stderr, exit, err
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sanitizeWinPSString strips characters that could break out of the quoted
// command context on the Windows shell line.
func sanitizeWinPSString(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '"', '\'', '`', '$', '&', '|', '<', '>', '(', ')', ';', '%', '^':
			continue
		case '\n', '\r':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

var _ = errors.New // keep errors import if unused later

// isWindowsMachine reports whether a machine targets Windows (used to pick
// pktmon over tcpdump for packet capture).
func isWindowsMachine(m db.Machine) bool {
	os := strings.ToLower(m.OS)
	if strings.Contains(os, "windows") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(m.Host), ".win") || m.Config["capture_mode"] == "pktmon"
}
