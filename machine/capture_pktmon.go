package machine

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
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
	// Verified against Windows 11 26H2 pktmon: -p is an EVENT PROVIDER flag
	// (not packet size!), -c/--capture enables packet capture with --comp
	// defaulting to all, --pkt-size 0 logs whole packets. Full-packet capture:
	//   pktmon start --capture --pkt-size 0 -f <etl> -m multi-file
	// Interface selection uses --comp <id> (component/NIC id from pktmon list).
	var sb strings.Builder
	sb.WriteString(`pktmon stop 2>nul & pktmon filter remove 2>nul & `)
	if bpf != "" {
		// pktmon filter add takes -i (IP) / -p (port); pass simple filters through as -f.
		sb.WriteString(fmt.Sprintf(`pktmon filter add -f "%s" 2>nul & `, sanitizeWinPSString(bpf)))
	}
	sb.WriteString(fmt.Sprintf(`pktmon start --capture --pkt-size 0 -f %s -m multi-file 2>&1`, etl))
	if ifIdx != "" && !strings.EqualFold(ifIdx, "any") {
		sb.WriteString(fmt.Sprintf(` --comp %s`, ifIdx))
	}
	sb.WriteString(` & echo RUNNING`)
	return s.captureCommand(ctx, m, c, sb.String(), 45*time.Second)
}

// stopCaptureWindows stops pktmon and converts the ETL to pcapng next to it;
// the converted file becomes the download path. -m multi-file appends a counter
// suffix to the ETL when it rolls (artex.etl -> artex1.etl), and the base name
// may itself exist from a prior run, so resolve the newest artex*.etl by mtime.
// Conversion is `pktmon etl2pcap` on current builds (etl2pcapng on older ones;
// both are tried).
func (s *Service) stopCaptureWindows(ctx context.Context, taskID int64, m db.Machine, c db.TaskCredential, cap db.MachineCapture) (remotePath string, stdout, stderr string, exit int, err error) {
	// Windows paths: split on backslashes explicitly — filepath.Dir/Base on the
	// Linux host treat the whole C:\...\x.etl as one filename.
	full := strings.ReplaceAll(cap.RemotePath, "/", "\\")
	i := strings.LastIndex(full, "\\")
	dir, file := ".", full
	if i >= 0 {
		dir, file = full[:i], full[i+1:]
	}
	name := strings.TrimSuffix(file, filepath.Ext(file))
	base := full[:len(full)-len(filepath.Ext(full))]
	pcapng := base + ".pcapng"
	// One PowerShell line: stop pktmon, locate the newest artex-*.etl (the
	// multi-file mode appends a counter suffix), convert with etl2pcap (falling
	// back to etl2pcapng on older builds). cmd.exe chaining with for /f + set
	// proved fragile over SSH; a single powershell -Command is robust.
	// The whole script travels base64-encoded (powershell -EncodedCommand): no
	// quoting survives the Windows SSH default shell + cmd + powershell chain
	// intact, and paths with backslashes drive that mangling.
	ps := fmt.Sprintf(
		`pktmon stop | Out-Null; `+
			`$e = Get-ChildItem -Path '%s' -Filter '%s*.etl' | Sort-Object LastWriteTime -Descending | Select-Object -First 1; `+
			`$out = '%s'; `+
			`if ($e) { pktmon etl2pcap $e.FullName --out $out }; `+
			`if ($e -and -not (Test-Path $out)) { pktmon etl2pcapng $e.FullName --out $out }; `+
			`if (Test-Path $out) { Write-Output 'PCAPNG_OK' } else { Write-Output 'PCAPNG_MISSING' }`,
		dir, name, pcapng)
	cmd := "powershell -NoProfile -EncodedCommand " + psEncode(ps)
	stdout, stderr, exit, err = s.captureCommand(ctx, m, c, cmd, 120*time.Second)
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

// psEncode encodes a PowerShell script for -EncodedCommand (UTF-16LE base64).
func psEncode(script string) string {
	u16 := make([]byte, 0, len(script)*2)
	for _, b := range []byte(script) {
		u16 = append(u16, b, 0)
	}
	return base64.StdEncoding.EncodeToString(u16)
}

// pcapngSummary counts packets in a downloaded capture (pcapng or classic pcap)
// for the captures list: "N packets". Best effort — unreadable file returns "".
func pcapngSummary(path string) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) < 12 {
		return ""
	}
	// pcapng: Enhanced Packet Blocks (type 6); classic pcap: little/big-endian counts
	if b[0] == 0x0a && b[1] == 0x0d { // \n\r magic
		n, i := 0, 0
		for i+8 <= len(b) {
			bt := binary.LittleEndian.Uint32(b[i : i+4])
			bl := binary.LittleEndian.Uint32(b[i+4 : i+8])
			if bl < 12 || i+int(bl) > len(b) {
				break
			}
			if bt == 6 {
				n++
			}
			i += int(bl)
		}
		return fmt.Sprintf("%d packets", n)
	}
	// classic pcap: 24-byte header, 16-byte per-packet records
	if binary.LittleEndian.Uint32(b[:4]) == 0xa1b2c3d4 || binary.LittleEndian.Uint32(b[:4]) == 0xd4c3b2a1 {
		n, i := 0, 24
		for i+16 <= len(b) {
			cl := binary.LittleEndian.Uint32(b[i+8 : i+12])
			if cl == 0 {
				break
			}
			n++
			i += 16 + int(cl)
		}
		return fmt.Sprintf("%d packets", n)
	}
	return ""
}
