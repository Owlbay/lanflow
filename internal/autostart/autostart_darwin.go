package autostart

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// home is the user's home directory (a variable for tests).
var home = os.UserHomeDir

func plistPath() (string, error) {
	h, err := home()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, "Library", "LaunchAgents", Label+".plist"), nil
}

// plist is the launch agent: run at load (login) once, in the user's GUI
// session, not kept alive (退出澜流 must stay quit).
func plist(exe string) []byte {
	esc := func(s string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	var args bytes.Buffer
	for _, a := range append([]string{exe}, Args...) {
		args.WriteString("\t\t<string>" + esc(a) + "</string>\n")
	}
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + Label + `</string>
	<key>ProgramArguments</key>
	<array>
` + args.String() + `	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
	<key>LimitLoadToSessionType</key>
	<string>Aqua</string>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`)
}

func apply(exe string, on bool) (bool, error) {
	path, err := plistPath()
	if err != nil {
		return false, err
	}
	if !on {
		err := os.Remove(path)
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	}
	want := plist(exe)
	if have, err := os.ReadFile(path); err == nil && bytes.Equal(have, want) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, want, 0o644)
}

func enabled(exe string) bool {
	path, err := plistPath()
	if err != nil {
		return false
	}
	have, err := os.ReadFile(path)
	return err == nil && bytes.Equal(have, plist(exe))
}
