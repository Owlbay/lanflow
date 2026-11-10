package view

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // group icons may be JPEG
	"image/png"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// Terminal proxy commands for the tray's 复制终端代理命令 submenu, one per
// shell family (docs/design.md §7).

// shellCommand is one variant of the terminal proxy command.
type shellCommand struct {
	Label string // shown in the menu
	Cmd   string // copied to the clipboard
}

// shellCommands returns the variants for port: bash/zsh, fish, PowerShell
// and cmd.
func shellCommands(port int) []shellCommand {
	if port <= 0 {
		port = 7890
	}
	h := fmt.Sprintf("http://127.0.0.1:%d", port)
	s := fmt.Sprintf("socks5://127.0.0.1:%d", port)
	return []shellCommand{
		{"bash / zsh", fmt.Sprintf("export https_proxy=%s http_proxy=%s all_proxy=%s", h, h, s)},
		{"fish", fmt.Sprintf("set -gx https_proxy %s; set -gx http_proxy %s; set -gx all_proxy %s", h, h, s)},
		{"PowerShell", fmt.Sprintf(`$env:HTTPS_PROXY="%s"; $env:HTTP_PROXY="%s"; $env:ALL_PROXY="%s"`, h, h, s)},
		{"cmd", fmt.Sprintf("set https_proxy=%s && set http_proxy=%s && set all_proxy=%s", h, h, s)},
	}
}

// menuIconPNG renders icon bytes (SVG, PNG or JPEG) as a small PNG for a
// menu item; nil when the data cannot be drawn.
func menuIconPNG(data []byte, size int) []byte {
	trimmed := bytes.TrimSpace(data)
	var img image.Image
	if bytes.HasPrefix(trimmed, []byte("<svg")) || bytes.HasPrefix(trimmed, []byte("<?xml")) {
		s, err := ui.ParseSVG(trimmed)
		if err != nil {
			return nil
		}
		img = s.Image(size, size, color.NRGBA{0, 0, 0, 255})
	} else {
		decoded, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil
		}
		img = decoded
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

// menuIcon returns the menu icon of a group icon reference, cached by
// reference; nil while the data is not loaded or cannot be drawn.
func (tr *tray) menuIcon(ref string) *mygo.MenuIcon {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	tr.mu.Lock()
	if ic, ok := tr.icons[ref]; ok {
		tr.mu.Unlock()
		return ic
	}
	tr.mu.Unlock()
	data, ok := tr.h.hostAPI().IconData(ref)
	if !ok {
		return nil
	}
	var ic *mygo.MenuIcon
	if pngData := menuIconPNG(data, 32); pngData != nil {
		ic, _ = mygo.NewMenuIcon(pngData, false)
	}
	tr.mu.Lock()
	if tr.icons == nil {
		tr.icons = map[string]*mygo.MenuIcon{}
	}
	tr.icons[ref] = ic // nil too: never retried
	tr.mu.Unlock()
	return ic
}
