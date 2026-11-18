package machine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// sessionOf returns the live desktop session of a machine.
func sessionOf(name string) (*desktopSession, error) {
	desktopMu.Lock()
	defer desktopMu.Unlock()
	sess, ok := desktopSessions[name]
	if !ok || !sess.alive() {
		if ok {
			sess.close()
			delete(desktopSessions, name)
		}
		return nil, fmt.Errorf("no desktop session on %s; open one with desktop_open first", name)
	}
	return sess, nil
}

// DesktopScreen captures the console, OCRs it and stores the PNG as evidence.
func (s *Service) DesktopScreen(ctx context.Context, taskID int64, name string) (map[string]any, error) {
	sess, err := sessionOf(name)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	png, w, h, err := sess.grabPNG(ctx)
	if err != nil {
		return nil, err
	}
	text, ocrErr := ocr(ctx, png)
	m, _ := s.Machine(name)
	s.recordDesktop(m, "desktop_screen", fmt.Sprintf("%dx%d", w, h), text, time.Since(started).Milliseconds(), png)
	out := map[string]any{
		"machine": name, "width": w, "height": h,
		"png_bytes": len(png), "text": text,
	}
	if ocrErr != nil {
		out["ocr"] = "unavailable: " + ocrErr.Error()
	}
	return out, nil
}

// DesktopClick clicks at console coordinates (absolute).
func (s *Service) DesktopClick(ctx context.Context, taskID int64, name string, x, y int, button string) (map[string]any, error) {
	sess, err := sessionOf(name)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	mask := 0
	switch button {
	case "", "left":
		mask = 1
	case "middle":
		mask = 2
	case "right":
		mask = 4
	default:
		return nil, fmt.Errorf("button must be left|middle|right, not %q", button)
	}
	if err := sess.sendPointer(x, y, mask); err != nil {
		return nil, err
	}
	time.Sleep(40 * time.Millisecond)
	if err := sess.sendPointer(x, y, 0); err != nil {
		return nil, err
	}
	m, _ := s.Machine(name)
	s.recordDesktop(m, "desktop_click", fmt.Sprintf("%s @ %d,%d", button, x, y), "", time.Since(started).Milliseconds(), nil)
	return map[string]any{"machine": name, "x": x, "y": y, "button": button}, nil
}

// DesktopType types text through the console keyboard. For VNC this is a
// best-effort US-layout keysym walk; the RDP path uses xdotool.
func (s *Service) DesktopType(ctx context.Context, taskID int64, name string, text string) (map[string]any, error) {
	sess, err := sessionOf(name)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	if err := sess.typeText(ctx, text); err != nil {
		return nil, err
	}
	m, _ := s.Machine(name)
	s.recordDesktop(m, "desktop_type", fmt.Sprintf("%d chars", len(text)), text, time.Since(started).Milliseconds(), nil)
	return map[string]any{"machine": name, "typed": len([]rune(text))}, nil
}

// DesktopKey sends key names (enter, tab, esc, ctrl+alt+del, lwin, ...).
func (s *Service) DesktopKey(ctx context.Context, taskID int64, name string, keys string) (map[string]any, error) {
	sess, err := sessionOf(name)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	if err := sess.sendKeys(ctx, keys); err != nil {
		return nil, err
	}
	m, _ := s.Machine(name)
	s.recordDesktop(m, "desktop_key", keys, "", time.Since(started).Milliseconds(), nil)
	return map[string]any{"machine": name, "keys": keys}, nil
}

// DesktopClose closes the session.
func (s *Service) DesktopClose(ctx context.Context, name string) (map[string]any, error) {
	desktopMu.Lock()
	sess, ok := desktopSessions[name]
	if ok {
		delete(desktopSessions, name)
	}
	desktopMu.Unlock()
	if !ok {
		return map[string]any{"machine": name, "closed": false}, nil
	}
	sess.close()
	m, _ := s.Machine(name)
	s.recordDesktop(m, "desktop_close", string(sess.kind), "", 0, nil)
	return map[string]any{"machine": name, "closed": true}, nil
}

// DesktopStatus reports the open sessions.
func (s *Service) DesktopStatus() []map[string]any {
	desktopMu.Lock()
	defer desktopMu.Unlock()
	out := make([]map[string]any, 0, len(desktopSessions))
	for _, sess := range desktopSessions {
		info := sess.info()
		info["alive"] = sess.alive()
		out = append(out, info)
	}
	return out
}

// sendPointer routes one pointer event by backend.
func (d *desktopSession) sendPointer(x, y, mask int) error {
	if d.kind == DesktopVNC {
		d.mu.Lock()
		conn := d.vnc
		d.mu.Unlock()
		if conn == nil {
			return errors.New("vnc session closed")
		}
		if x < 0 || y < 0 || x >= d.width || y >= d.height {
			return fmt.Errorf("pointer %d,%d outside the %dx%d console", x, y, d.width, d.height)
		}
		return conn.PointerEvent(x, y, mask)
	}
	return xdotoolErr(d.display, "mousemove", strconv.Itoa(x), strconv.Itoa(y))
}

// typeText types literal text on the chosen backend.
func (d *desktopSession) typeText(ctx context.Context, text string) error {
	if d.kind == DesktopRDP {
		args := []string{"type", "--", text}
		return xdotoolErr(d.display, args...)
	}
	d.mu.Lock()
	conn := d.vnc
	d.mu.Unlock()
	if conn == nil {
		return errors.New("vnc session closed")
	}
	for _, r := range text {
		for _, ks := range keysymsOf(r) {
			if err := conn.KeyEvent(ks, true); err != nil {
				return err
			}
			if err := conn.KeyEvent(ks, false); err != nil {
				return err
			}
			time.Sleep(8 * time.Millisecond)
		}
	}
	return nil
}

// sendKeys sends named keys/chords.
func (d *desktopSession) sendKeys(ctx context.Context, keys string) error {
	if d.kind == DesktopRDP {
		return xdotoolErr(d.display, "key", keys)
	}
	d.mu.Lock()
	conn := d.vnc
	d.mu.Unlock()
	if conn == nil {
		return errors.New("vnc session closed")
	}
	// VNC: split "ctrl+alt+del" into press-all then release-all.
	parts := strings.Split(keys, "+")
	var down []uint32
	for _, p := range parts {
		ks, ok := vncKeysyms[strings.ToLower(strings.TrimSpace(p))]
		if !ok {
			return fmt.Errorf("unknown key %q (vnc keys: enter tab esc space backspace delete ctrl alt shift lwin rwin up down left right home end pgup pgdn f1..f12)", p)
		}
		down = append(down, ks)
	}
	for _, ks := range down {
		if err := conn.KeyEvent(ks, true); err != nil {
			return err
		}
	}
	time.Sleep(30 * time.Millisecond)
	for i := len(down) - 1; i >= 0; i-- {
		if err := conn.KeyEvent(down[i], false); err != nil {
			return err
		}
	}
	return nil
}

// vncKeysyms maps key names to X11 keysyms.
var vncKeysyms = map[string]uint32{
	"enter": 0xff0d, "return": 0xff0d, "tab": 0xff09, "esc": 0xff1b, "escape": 0xff1b,
	"space": 0x20, "backspace": 0xff08, "delete": 0xffff, "del": 0xffff,
	"ctrl": 0xffe3, "alt": 0xffe9, "shift": 0xffe1,
	"lwin": 0xffeb, "super": 0xffeb, "rwin": 0xffec,
	"up": 0xff52, "down": 0xff54, "left": 0xff51, "right": 0xff53,
	"home": 0xff50, "end": 0xff57, "pgup": 0xff55, "pgdn": 0xff56,
}

// keysymsOf returns the keysym sequence for one rune (US layout): a printable
// ASCII char maps to its keysym; shifted symbols map to shift+key.
func keysymsOf(r rune) []uint32 {
	if r >= 0x20 && r <= 0x7e {
		lower := uint32(r)
		if r >= 'A' && r <= 'Z' {
			return []uint32{0xffe1, lower}
		}
		if s, ok := shiftedKeysyms[r]; ok {
			return []uint32{0xffe1, s}
		}
		return []uint32{lower}
	}
	return nil
}

var shiftedKeysyms = map[rune]uint32{
	'!': '1', '@': '2', '#': '3', '$': '4', '%': '5', '^': '6', '&': '7', '*': '8',
	'(': '9', ')': '0', '_': '-', '+': '=', '{': '[', '}': ']', ':': ';', '"': '\'',
	'<': ',', '>': '.', '?': '/', '|': '\\', '~': '`',
}

func init() {
	// f1..f12
	for i := 0; i < 12; i++ {
		vncKeysyms[fmt.Sprintf("f%d", i+1)] = 0xffbd + uint32(i)
	}
}

var _ = db.MachineEvent{}

// xdotoolErr runs xdotool against the session's display.
func xdotoolErr(display string, args ...string) error {
	cmd := exec.Command("xdotool", args...)
	cmd.Env = append(os.Environ(), "DISPLAY="+display)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("xdotool %v: %w: %s", args, err, strings.TrimSpace(string(out))[:min(200, len(out))])
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
