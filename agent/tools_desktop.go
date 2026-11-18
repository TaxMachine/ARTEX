package agent

import (
	"context"
	"encoding/json"

	actool "github.com/Autumn-27/norma/tool"
)

// Desktop console tools: drive a VM's graphical screen (QEMU VNC console or an
// RDP session) — screenshot with OCR text, click, type, keys. Every action is
// recorded as machine evidence with the screenshot stored under the machine's
// data dir. Bound to worker/pentest.

func (t *ToolSet) desktopOpen() actool.CoreTool {
	return writeTool("desktop_open",
		"Open (or reuse) the graphical console of a registered machine. kind=vnc attaches to the QEMU console (works at the firmware/logon/UAC screen; needs the machine's config.vnc_port) — no guest configuration. kind=rdp starts a real RDP session for Windows guests (needs an rdp-capable credential with username+password: a winrm or ssh password credential of the machine host). Returns the screen size; read it with desktop_screen.",
		obj(map[string]any{
			"machine":       str("registered machine name"),
			"kind":          map[string]any{"type": "string", "enum": []string{"vnc", "rdp"}, "description": "console backend (default vnc)"},
			"username":      str("rdp only: username (resolved from the task's password credentials when empty)"),
			"credential_id": idp("rdp only: task credential with the password"),
			"width":         intp("rdp only: session width (default 1280)"),
			"height":        intp("rdp only: session height (default 800)"),
		}, "machine"),
		t.needMachines("desktop_open", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine      string `json:"machine"`
				Kind         string `json:"kind"`
				Username     string `json:"username"`
				CredentialID int64  `json:"credential_id"`
				Width        int    `json:"width"`
				Height       int    `json:"height"`
			}
			_ = json.Unmarshal(in, &a)
			m, err := t.machines.Machine(a.Machine)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			opt := t.machines.DesktopOptionsFor(m)
			switch a.Kind {
			case "", "vnc":
				opt.Kind = "vnc"
			case "rdp":
				opt.Kind = "rdp"
				opt.Username = a.Username
				opt.Width, opt.Height = a.Width, a.Height
				cred, err := t.machines.RDPCredential(t.taskID, m, a.Username, a.CredentialID)
				if err != nil {
					return actool.Errorf("rdp credential: " + err.Error()), nil
				}
				opt.Username, opt.Password = cred.Username, cred.Password
			default:
				return actool.Errorf("kind must be vnc or rdp"), nil
			}
			out, err := t.machines.DesktopOpen(ctx, m, opt)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(out)
		}))
}

func (t *ToolSet) desktopScreen() actool.CoreTool {
	return readTool("desktop_screen",
		"Capture the machine's console as a screenshot and read it: the PNG is stored as machine evidence and the visible text comes back OCR'd in `text`. Use it to see what the screen shows (logon prompt, UAC dialog, desktop, error box) before clicking.",
		obj(map[string]any{"machine": str("registered machine name")}, "machine"),
		t.needMachines("desktop_screen", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ Machine string }
			_ = json.Unmarshal(in, &a)
			out, err := t.machines.DesktopScreen(ctx, t.taskID, a.Machine)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(out)
		}))
}

func (t *ToolSet) desktopClick() actool.CoreTool {
	return writeTool("desktop_click",
		"Click the console at absolute pixel coordinates. Take a desktop_screen first and compute the position from what you see; coordinates are pixels of the returned width/height.",
		obj(map[string]any{
			"machine": str("registered machine name"),
			"x":       intp("x in pixels"),
			"y":       intp("y in pixels"),
			"button":  map[string]any{"type": "string", "enum": []string{"left", "middle", "right"}, "description": "default left"},
		}, "machine", "x", "y"),
		t.needMachines("desktop_click", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine string `json:"machine"`
				X       int    `json:"x"`
				Y       int    `json:"y"`
				Button  string `json:"button"`
			}
			_ = json.Unmarshal(in, &a)
			out, err := t.machines.DesktopClick(ctx, t.taskID, a.Machine, a.X, a.Y, a.Button)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(out)
		}))
}

func (t *ToolSet) desktopType() actool.CoreTool {
	return writeTool("desktop_type",
		"Type literal text into the console's focused field (US keyboard layout). Click the field first.",
		obj(map[string]any{
			"machine": str("registered machine name"),
			"text":    str("the text to type"),
		}, "machine", "text"),
		t.needMachines("desktop_type", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine string `json:"machine"`
				Text    string `json:"text"`
			}
			_ = json.Unmarshal(in, &a)
			out, err := t.machines.DesktopType(ctx, t.taskID, a.Machine, a.Text)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(out)
		}))
}

func (t *ToolSet) desktopKey() actool.CoreTool {
	return writeTool("desktop_key",
		"Send key presses or chords to the console, e.g. enter, tab, esc, ctrl+alt+del, lwin, alt+f4, shift+tab. VNC knows: enter tab esc space backspace delete ctrl alt shift lwin rwin up down left right home end pgup pgdn f1..f12; RDP accepts anything xdotool does.",
		obj(map[string]any{
			"machine": str("registered machine name"),
			"keys":    str("key or chord, plus-separated"),
		}, "machine", "keys"),
		t.needMachines("desktop_key", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Machine string `json:"machine"`
				Keys    string `json:"keys"`
			}
			_ = json.Unmarshal(in, &a)
			out, err := t.machines.DesktopKey(ctx, t.taskID, a.Machine, a.Keys)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(out)
		}))
}

func (t *ToolSet) desktopClose() actool.CoreTool {
	return writeTool("desktop_close",
		"Close the machine's console session (VNC attach or the RDP/Xvfb pair). The VM keeps running.",
		obj(map[string]any{"machine": str("registered machine name")}, "machine"),
		t.needMachines("desktop_close", func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct{ Machine string }
			_ = json.Unmarshal(in, &a)
			out, err := t.machines.DesktopClose(ctx, a.Machine)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(out)
		}))
}
