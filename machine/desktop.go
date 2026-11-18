// machine/desktop implements agent interaction with a VM's graphical console:
// the QEMU VNC console (works at the firmware/logon/UAC screen, no guest
// configuration) and RDP sessions for Windows guests (xfreerdp under Xvfb, with
// clipboard and better input fidelity). Screenshots are OCR-able so an agent
// can read the screen; every action is recorded as machine evidence.
package machine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/internal/rfb"
)

// DesktopKind selects the console backend of one desktop session.
type DesktopKind string

const (
	DesktopVNC DesktopKind = "vnc"
	DesktopRDP DesktopKind = "rdp"
)

// DesktopOptions opens one desktop session.
type DesktopOptions struct {
	Kind     DesktopKind
	Host     string // vnc: host:port of the console; rdp: host[:port] (3389 default)
	Port     int    // 0 = vnc 5900 / rdp 3389
	Username string // rdp only
	Password string // rdp only (from the task credential)
	Domain   string
	Width    int // rdp session size (default 1280x800)
	Height   int
}

// desktopSession is one live console: a VNC socket or an Xvfb+xfreerdp pair.
type desktopSession struct {
	mu         sync.Mutex
	kind       DesktopKind
	machine    db.Machine
	vnc        *rfb.Conn
	xvfbCmd    *exec.Cmd
	freerdpCmd *exec.Cmd
	display    string // rdp: the Xvfb display number
	width      int
	height     int
	openedAt   time.Time
	xauth      string // rdp: the generated Xauthority file
}

var (
	desktopMu       sync.Mutex
	desktopSessions = map[string]*desktopSession{} // machine name -> session
)

// maxDesktopSessions bounds concurrent consoles (each RDP session costs a
// full framebuffer and an Xvfb).
const maxDesktopSessions = 4

// DesktopOpen opens (or reuses) the machine's desktop console. A VNC console
// needs the machine's registered vnc_port (config or default 5900+offset); an
// RDP session needs credentials and a reachable 3389.
func (s *Service) DesktopOpen(ctx context.Context, m db.Machine, opt DesktopOptions) (map[string]any, error) {
	desktopMu.Lock()
	defer desktopMu.Unlock()
	if cur, ok := desktopSessions[m.Name]; ok {
		if cur.alive() {
			return cur.info(), nil
		}
		cur.close()
		delete(desktopSessions, m.Name)
	}
	if len(desktopSessions) >= maxDesktopSessions {
		return nil, fmt.Errorf("desktop session limit reached (%d); close one first", maxDesktopSessions)
	}

	var sess *desktopSession
	var err error
	switch opt.Kind {
	case DesktopVNC:
		sess, err = openVNC(m, opt)
	case DesktopRDP:
		sess, err = s.openRDP(ctx, m, opt)
	default:
		return nil, fmt.Errorf("unknown desktop kind %q (vnc|rdp)", opt.Kind)
	}
	if err != nil {
		return nil, err
	}
	desktopSessions[m.Name] = sess
	s.recordDesktop(m, "desktop_open", string(opt.Kind), "", 0, nil)
	return sess.info(), nil
}

func openVNC(m db.Machine, opt DesktopOptions) (*desktopSession, error) {
	port := opt.Port
	if port == 0 {
		port = configInt(m, "vnc_port", 0)
	}
	if port == 0 {
		return nil, errors.New("the machine has no vnc_port configured; add config.vnc_port (QEMU console) or use rdp")
	}
	host := opt.Host
	if host == "" {
		host = m.Host
	}
	conn, err := rfb.Dial(netJoinHostPort(host, port), 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("vnc %s:%d: %w", host, port, err)
	}
	w, h := conn.Size()
	return &desktopSession{kind: DesktopVNC, machine: m, vnc: conn, width: w, height: h, openedAt: time.Now()}, nil
}

func netJoinHostPort(host string, port int) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return fmt.Sprintf("[%s]:%d", host, port)
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// openRDP starts Xvfb + xfreerdp and waits for the session bitmap to appear.
func (s *Service) openRDP(ctx context.Context, m db.Machine, opt DesktopOptions) (*desktopSession, error) {
	for _, bin := range []string{"Xvfb", "xfreerdp", "xdotool", "import", "tesseract"} {
		if _, err := exec.LookPath(bin); err != nil {
			return nil, fmt.Errorf("RDP needs %s installed in the ARTEX container", bin)
		}
	}
	host := opt.Host
	if host == "" {
		host = m.Host
	}
	port := opt.Port
	if port == 0 {
		port = 3389
	}
	w, h := opt.Width, opt.Height
	if w == 0 {
		w = 1280
	}
	if h == 0 {
		h = 800
	}
	if opt.Username == "" || opt.Password == "" {
		return nil, errors.New("rdp needs username and password (a winrm/ssh credential with a password)")
	}

	// Xvfb on a free display, private auth.
	display, authFile, xvfb, err := startHiddenXvfb(w, h)
	if err != nil {
		return nil, fmt.Errorf("xvfb: %w", err)
	}
	sess := &desktopSession{kind: DesktopRDP, machine: m, xvfbCmd: xvfb, display: display, xauth: authFile, width: w, height: h, openedAt: time.Now()}

	// xfreerdp /v: /u: /p: — with a bounded connect timeout.
	args := []string{
		fmt.Sprintf("/v:%s:%d", host, port),
		fmt.Sprintf("/u:%s", opt.Username),
		fmt.Sprintf("/p:%s", opt.Password),
		fmt.Sprintf("/size:%dx%d", w, h),
		"/cert:ignore", "/dynamic-resolution", "/clipboard",
		"+fonts",
	}
	if opt.Domain != "" {
		args = append(args, "/d:"+opt.Domain)
	}
	rdp := exec.Command("xfreerdp", args...)
	rdp.Env = append(os.Environ(), "DISPLAY="+display, "XAUTHORITY="+authFile)
	if err := rdp.Start(); err != nil {
		sess.close()
		return nil, fmt.Errorf("xfreerdp: %w", err)
	}
	sess.freerdpCmd = rdp

	// Wait until the session renders: import must produce a non-black bitmap.
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if rdp.ProcessState != nil {
			sess.close()
			return nil, errors.New("xfreerdp exited early (bad credentials or RDP not enabled on the guest)")
		}
		if png, _, _, err := sess.grabPNG(ctx); err == nil && !isMostlyBlack(png) {
			return sess, nil
		}
		time.Sleep(700 * time.Millisecond)
	}
	sess.close()
	return nil, errors.New("RDP session did not render within 45s (check credentials / guest RDP)")
}

func startHiddenXvfb(w, h int) (string, string, *exec.Cmd, error) {
	// Find a free display number by socket absence; 20..80 keeps clear of :0.
	for n := 20; n < 80; n++ {
		display := fmt.Sprintf(":%d", n)
		socket := fmt.Sprintf("/tmp/.X11-unix/X%d", n)
		if _, err := os.Stat(socket); err == nil {
			continue
		}
		authFile := filepath.Join(os.TempDir(), fmt.Sprintf("artex-xauth-%d-%d", os.Getpid(), n))
		if err := exec.Command("xauth", "-f", authFile, "add", display, ".", "MIT-MAGIC-COOKIE-1", hexRand32()).Run(); err != nil {
			return "", "", nil, fmt.Errorf("xauth: %w", err)
		}
		cmd := exec.Command("Xvfb", display, "-screen", "0", fmt.Sprintf("%dx%dx24", w, h), "-nolisten", "tcp", "-auth", authFile)
		if err := cmd.Start(); err != nil {
			return "", "", nil, err
		}
		// Wait for the socket.
		for i := 0; i < 20; i++ {
			if _, err := os.Stat(socket); err == nil {
				return display, authFile, cmd, nil
			}
			time.Sleep(100 * time.Millisecond)
		}
		_ = cmd.Process.Kill()
		return "", "", nil, errors.New("Xvfb socket did not appear")
	}
	return "", "", nil, errors.New("no free X display")
}

func hexRand32() string {
	var b [16]byte
	if _, err := readRand(b[:]); err != nil {
		return "00112233445566778899aabbccddeeff"
	}
	const hex = "0123456789abcdef"
	out := make([]byte, 0, 32)
	for _, x := range b {
		out = append(out, hex[x>>4], hex[x&0xf])
	}
	return string(out)
}

func readRand(b []byte) (int, error) {
	f, err := os.Open("/dev/urandom")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.Read(b)
}

// alive reports whether the session's backend is still up.
func (d *desktopSession) alive() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch d.kind {
	case DesktopVNC:
		return d.vnc != nil
	case DesktopRDP:
		return d.freerdpCmd != nil && d.freerdpCmd.ProcessState == nil
	}
	return false
}

func (d *desktopSession) info() map[string]any {
	return map[string]any{
		"machine": d.machine.Name,
		"kind":    string(d.kind),
		"width":   d.width,
		"height":  d.height,
		"opened":  d.openedAt.Format(time.RFC3339),
	}
}

func (d *desktopSession) close() {
	if d.vnc != nil {
		_ = d.vnc.Close()
		d.vnc = nil
	}
	if d.freerdpCmd != nil && d.freerdpCmd.Process != nil {
		_ = d.freerdpCmd.Process.Kill()
		_, _ = d.freerdpCmd.Process.Wait()
		d.freerdpCmd = nil
	}
	if d.xvfbCmd != nil && d.xvfbCmd.Process != nil {
		_ = d.xvfbCmd.Process.Kill()
		_, _ = d.xvfbCmd.Process.Wait()
		d.xvfbCmd = nil
	}
	if d.xauth != "" {
		_ = os.Remove(d.xauth)
	}
}

// grabPNG captures one screenshot as PNG bytes.
func (d *desktopSession) grabPNG(ctx context.Context) ([]byte, int, int, error) {
	if d.kind == DesktopVNC {
		d.mu.Lock()
		conn := d.vnc
		d.mu.Unlock()
		if conn == nil {
			return nil, 0, 0, errors.New("vnc session closed")
		}
		rgba, err := conn.Screen()
		if err != nil {
			return nil, 0, 0, err
		}
		w, h := conn.Size()
		img := &image.RGBA{Pix: rgba, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, 0, 0, err
		}
		return buf.Bytes(), w, h, nil
	}
	// RDP: capture the Xvfb display.
	out, err := exec.CommandContext(ctx, "import", "-window", "root", "-display", d.display, "png:-").Output()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("import: %w", err)
	}
	return out, d.width, d.height, nil
}

// isMostlyBlack rejects the pre-session blank screen.
func isMostlyBlack(png []byte) bool {
	if len(png) < 4096 {
		return true
	}
	// A blank screen compresses to almost nothing.
	return len(png) < 20_000
}

var ocrSafe = regexp.MustCompile(`^[A-Za-z0-9 \.,:/\\_\-"'()#%+]+$`)

// ocr runs tesseract over a PNG and returns its text.
func ocr(ctx context.Context, png []byte) (string, error) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		return "", errors.New("tesseract is not installed in the ARTEX container")
	}
	tmp, err := os.CreateTemp("", "artex-ocr-*.png")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(png); err != nil {
		tmp.Close()
		return "", err
	}
	tmp.Close()
	out, err := exec.CommandContext(ctx, "tesseract", tmp.Name(), "stdout", "--psm", "6").Output()
	if err != nil {
		return "", fmt.Errorf("tesseract: %w", err)
	}
	return string(out), nil
}

// recordDesktop writes one desktop action as machine evidence. png is stored
// under the machine's data dir when non-nil.
func (s *Service) recordDesktop(m db.Machine, action, detail, text string, durMS int64, png []byte) {
	var path string
	if len(png) > 0 {
		dir := filepath.Join(s.dataDir, "machine_desktop", safeBase(m.Name))
		if err := os.MkdirAll(dir, 0o755); err == nil {
			name := fmt.Sprintf("%s-%d.png", action, time.Now().UnixMilli())
			full := filepath.Join(dir, name)
			if err := os.WriteFile(full, png, 0o644); err == nil {
				path = full
			}
		}
	}
	var task *int64
	exit := 0
	_, _ = s.record(db.MachineEvent{
		TaskID: task, MachineID: &m.ID, Machine: m.Name, Protocol: "desktop", Action: action,
		Command: detail, Stdout: truncate(text, storedOutputCap), ExitCode: &exit,
		DurationMS: int(durMS), Bytes: int64(len(png)),
		Meta: map[string]any{"screenshot": path},
	})
}

// DesktopOptionsFor builds default options from the machine row.
func (s *Service) DesktopOptionsFor(m db.Machine) DesktopOptions {
	return DesktopOptions{
		Kind: DesktopVNC,
		Host: m.Host,
		Port: configInt(m, "vnc_port", 0),
	}
}

// RDPCredential resolves an RDP login for a machine: an explicit credential id
// (ssh/winrm with a password), else the newest password credential of the
// machine host/name. Key credentials cannot log into RDP.
func (s *Service) RDPCredential(taskID int64, m db.Machine, username string, credentialID int64) (db.TaskCredential, error) {
	if taskID <= 0 {
		return db.TaskCredential{}, errors.New("desktop tools need a running task")
	}
	if credentialID > 0 {
		c, err := s.pg.Credential(taskID, credentialID)
		if err != nil {
			return c, fmt.Errorf("credential %d not found in this task", credentialID)
		}
		if c.Password == "" {
			return c, errors.New("the credential has no password; RDP needs password auth")
		}
		return c, nil
	}
	list, err := s.pg.TaskCredentials(taskID)
	if err != nil {
		return db.TaskCredential{}, err
	}
	host := strings.ToLower(strings.TrimSpace(m.Host))
	name := strings.ToLower(strings.TrimSpace(m.Name))
	var best *db.TaskCredential
	for i := range list {
		c := &list[i]
		if c.Protocol != "winrm" && c.Protocol != "ssh" {
			continue
		}
		if c.Password == "" || c.SecretType != "password" {
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
			"no password credential for RDP on machine %s (host %s). Register one with save_task_credential(protocol=\"winrm\", domain=\"%s\", username=\"...\", password=\"...\")",
			m.Name, m.Host, m.Host)
	}
	return *best, nil
}
