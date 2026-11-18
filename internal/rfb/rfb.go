// Package rfb implements the minimal RFB (VNC) client the machine desktop
// layer needs: framebuffer updates for screenshots and pointer/key events for
// input. Only the RAW and CopyRect encodings plus a hard cap on the rectangle
// count are supported, which QEMU's VNC server provides by default.
package rfb

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	encRaw      = 0
	encCopyRect = 1
)

// Conn is one RFB client connection.
type Conn struct {
	c        net.Conn
	r        *bufio.Reader
	w        *bufio.Writer
	name     string
	width    int
	height   int
	PerPixel int // bytes per pixel as negotiated (4 for QEMU's default)
	// red/green/blue bit shifts of the negotiated pixel format.
	RShift, GShift, BShift uint
	Bpp16                  bool
}

// Dial connects, performs the RFB handshake and requests the incremental
// framebuffer format QEMU serves. secNone only: the lab console socket is
// loopback/pin-gated, and VNC auth adds nothing there.
func Dial(addr string, timeout time.Duration) (*Conn, error) {
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Now().Add(timeout))
	conn := &Conn{c: c, r: bufio.NewReader(c), w: bufio.NewWriter(c)}
	if err := conn.handshake(); err != nil {
		_ = c.Close()
		return nil, err
	}
	// Screen reads get their own generous deadline; input writes stay snappy.
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	_ = c.SetWriteDeadline(time.Time{})
	return conn, nil
}

func (f *Conn) handshake() error {
	ver, err := f.r.ReadSlice('\n')
	if err != nil || len(ver) < 12 {
		return fmt.Errorf("rfb: server version %q: %v", ver, err)
	}
	// Answer with the same major.minor the server offered.
	if _, err := f.w.Write([]byte("RFB 003.008\n")); err != nil {
		return err
	}
	if err := f.w.Flush(); err != nil {
		return err
	}
	// Security types: one byte count, then types; 00 means failure follows.
	n, err := f.r.ReadByte()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("rfb: server refused the handshake")
	}
	types := make([]byte, n)
	if _, err := io.ReadFull(f.r, types); err != nil {
		return err
	}
	hasNone := false
	for _, t := range types {
		if t == 1 {
			hasNone = true
		}
	}
	if !hasNone {
		return fmt.Errorf("rfb: server requires auth (types %v); only None is supported", types)
	}
	if err := f.w.WriteByte(1); err != nil { // choose None
		return err
	}
	if err := f.w.Flush(); err != nil {
		return err
	}
	var code uint32
	if err := binary.Read(f.r, binary.BigEndian, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("rfb: security handshake failed (%d)", code)
	}
	// ClientInit: shared.
	if err := f.w.WriteByte(1); err != nil {
		return err
	}
	if err := f.w.Flush(); err != nil {
		return err
	}
	// ServerInit.
	var w, h uint16
	if err := binary.Read(f.r, binary.BigEndian, &w); err != nil {
		return err
	}
	if err := binary.Read(f.r, binary.BigEndian, &h); err != nil {
		return err
	}
	pf := make([]byte, 16)
	if _, err := io.ReadFull(f.r, pf); err != nil {
		return err
	}
	bpp := pf[0]
	depth := pf[1]
	if (bpp != 32 && bpp != 16) || depth == 0 {
		return fmt.Errorf("rfb: unsupported pixel format bpp=%d depth=%d", bpp, depth)
	}
	var nameLen uint32
	if err := binary.Read(f.r, binary.BigEndian, &nameLen); err != nil {
		return err
	}
	if nameLen > 4096 {
		return errors.New("rfb: unreasonable desktop name length")
	}
	name := make([]byte, nameLen)
	if _, err := io.ReadFull(f.r, name); err != nil {
		return err
	}
	f.width, f.height, f.name = int(w), int(h), string(name)
	f.PerPixel = int(bpp / 8)
	f.Bpp16 = bpp == 16
	// big-endian flag, then the true-color shifts QEMU reports.
	f.RShift, f.GShift, f.BShift = uint(pf[10]), uint(pf[11]), uint(pf[12])
	return nil
}

// Size returns the framebuffer dimensions.
func (f *Conn) Size() (int, int) { return f.width, f.height }

// Name returns the desktop name the server sent.
func (f *Conn) Name() string { return f.name }

// maxRects bounds one update; a malicious server cannot make us allocate at
// will. QEMU sends full-frame rectangles in practice.
const maxRects = 512

// Screen fetches one full framebuffer update into an RGBA byte slice
// (w*h*4, row-major). incremental=false forces a complete refresh.
func (f *Conn) Screen() ([]byte, error) {
	_ = f.c.SetReadDeadline(time.Now().Add(30 * time.Second))
	// FramebufferUpdateRequest: full, non-incremental.
	req := []byte{3, 0, 0, 0, 0, 0,
		byte(f.width >> 8), byte(f.width),
		byte(f.height >> 8), byte(f.height)}
	if _, err := f.w.Write(req); err != nil {
		return nil, err
	}
	if err := f.w.Flush(); err != nil {
		return nil, err
	}
	buf := make([]byte, f.width*f.height*4)
	// CopyRect sources need the previous frame; keep one per call for correctness.
	var prev []byte
	for {
		// MessageType 0 = FramebufferUpdate.
		t, err := f.r.ReadByte()
		if err != nil {
			return nil, err
		}
		if t != 0 {
			// Skip everything else the server may send (Bell=2, CutText=3…).
			if err := f.skipMessage(t); err != nil {
				return nil, err
			}
			continue
		}
		_, _ = f.r.ReadByte() // padding
		var nRect uint16
		if err := binary.Read(f.r, binary.BigEndian, &nRect); err != nil {
			return nil, err
		}
		if nRect > maxRects {
			return nil, fmt.Errorf("rfb: update announces %d rectangles (cap %d)", nRect, maxRects)
		}
		for i := 0; i < int(nRect); i++ {
			var x, y, w, h uint16
			if err := binary.Read(f.r, binary.BigEndian, &x); err != nil {
				return nil, err
			}
			if err := binary.Read(f.r, binary.BigEndian, &y); err != nil {
				return nil, err
			}
			if err := binary.Read(f.r, binary.BigEndian, &w); err != nil {
				return nil, err
			}
			if err := binary.Read(f.r, binary.BigEndian, &h); err != nil {
				return nil, err
			}
			var enc int32
			if err := binary.Read(f.r, binary.BigEndian, &enc); err != nil {
				return nil, err
			}
			if int(x)+int(w) > f.width || int(y)+int(h) > f.height || w == 0 || h == 0 {
				return nil, fmt.Errorf("rfb: rectangle %dx%d at (%d,%d) outside the framebuffer", w, h, x, y)
			}
			switch enc {
			case encRaw:
				row := make([]byte, int(w)*f.PerPixel)
				for r := 0; r < int(h); r++ {
					if _, err := io.ReadFull(f.r, row); err != nil {
						return nil, err
					}
					f.copyRow(buf, row, int(x), int(y)+r, int(w))
				}
			case encCopyRect:
				if prev == nil {
					prev = make([]byte, len(buf))
					copy(prev, buf)
				}
				var sx, sy uint16
				if err := binary.Read(f.r, binary.BigEndian, &sx); err != nil {
					return nil, err
				}
				if err := binary.Read(f.r, binary.BigEndian, &sy); err != nil {
					return nil, err
				}
				for r := 0; r < int(h); r++ {
					src := (int(sy)+r)*f.width + int(sx)
					dst := (int(y)+r)*f.width + int(x)
					for c := 0; c < int(w); c++ {
						copy(buf[(dst+c)*4:(dst+c)*4+4], prev[(src+c)*4:(src+c)*4+4])
					}
				}
			default:
				return nil, fmt.Errorf("rfb: unsupported encoding %d (enable RAW on the server)", enc)
			}
		}
		return buf, nil
	}
}

// copyRow converts one RAW row of server pixels into RGBA at dstX,dstY.
func (f *Conn) copyRow(buf, row []byte, dstX, dstY, w int) {
	base := dstY*f.width + dstX
	for i := 0; i < w; i++ {
		var r, g, b uint8
		switch {
		case f.Bpp16:
			v := binary.BigEndian.Uint16(row[i*2:])
			r = uint8((v >> f.RShift) & 0xff)
			g = uint8((v >> f.GShift) & 0xff)
			b = uint8((v >> f.BShift) & 0xff)
		default:
			v := binary.BigEndian.Uint32(row[i*4:])
			r = uint8((v >> f.RShift) & 0xff)
			g = uint8((v >> f.GShift) & 0xff)
			b = uint8((v >> f.BShift) & 0xff)
		}
		o := (base + i) * 4
		buf[o], buf[o+1], buf[o+2], buf[o+3] = r, g, b, 0xff
	}
}

// skipMessage reads and drops one non-update server message.
func (f *Conn) skipMessage(t byte) error {
	switch t {
	case 1: // SetColourMapEntries: pad + first + n
		var b [5]byte
		if _, err := io.ReadFull(f.r, b[:]); err != nil {
			return err
		}
		n := binary.BigEndian.Uint16(b[3:])
		_, err := io.CopyN(io.Discard, f.r, int64(n)*6)
		return err
	case 2: // Bell: no payload
		return nil
	case 3: // ServerCutText
		var b [7]byte
		if _, err := io.ReadFull(f.r, b[:]); err != nil {
			return err
		}
		n := binary.BigEndian.Uint32(b[3:])
		_, err := io.CopyN(io.Discard, f.r, int64(n))
		return err
	default:
		return fmt.Errorf("rfb: unknown server message %d", t)
	}
}

// PointerEvent sends one absolute mouse event. buttonMask bit 0 = left,
// 1 = middle, 2 = right, 3+ = wheel.
func (f *Conn) PointerEvent(x, y, mask int) error {
	if _, err := f.w.Write([]byte{5, byte(mask),
		byte(x >> 8), byte(x), byte(y >> 8), byte(y)}); err != nil {
		return err
	}
	return f.w.Flush()
}

// KeyEvent sends one key event. keysym is an X11 keysym (e.g. 0xff0d Enter,
// 'A' = 0x41); down selects press or release.
func (f *Conn) KeyEvent(keysym uint32, down bool) error {
	d := byte(0)
	if down {
		d = 1
	}
	var b [8]byte
	b[0], b[1] = 4, d
	binary.BigEndian.PutUint32(b[4:], keysym)
	if _, err := f.w.Write(b[:]); err != nil {
		return err
	}
	return f.w.Flush()
}

// ClientCutText announces the server clipboard (unused, kept for completeness).
func (f *Conn) ClientCutText(text string) error {
	var h [7]byte
	h[0] = 6
	binary.BigEndian.PutUint32(h[3:], uint32(len(text)))
	if _, err := f.w.Write(h[:]); err != nil {
		return err
	}
	if _, err := f.w.WriteString(text); err != nil {
		return err
	}
	return f.w.Flush()
}

// Close closes the connection.
func (f *Conn) Close() error { return f.c.Close() }
