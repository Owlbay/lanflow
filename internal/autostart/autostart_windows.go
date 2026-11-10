package autostart

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// The Run value of the user; Task Manager's Startup page turns it off in
// StartupApproved without removing it.
const (
	runKey      = `Software\Microsoft\Windows\CurrentVersion\Run`
	approvedKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run`
)

func command(exe string) string { return `"` + exe + `" ` + strings.Join(Args, " ") }

func apply(exe string, on bool) (bool, error) {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	have, _, herr := k.GetStringValue(Label)
	if !on {
		if herr != nil {
			return false, nil
		}
		return true, k.DeleteValue(Label)
	}
	changed := false
	if herr != nil || !strings.EqualFold(have, command(exe)) {
		if err := k.SetStringValue(Label, command(exe)); err != nil {
			return false, err
		}
		changed = true
	}
	// Turned off in Task Manager earlier: the user asks for it again now.
	if a, err := registry.OpenKey(registry.CURRENT_USER, approvedKey, registry.SET_VALUE); err == nil {
		if a.DeleteValue(Label) == nil {
			changed = true
		}
		a.Close()
	}
	return changed, nil
}

func enabled(exe string) bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	have, _, err := k.GetStringValue(Label)
	if err != nil || !strings.EqualFold(have, command(exe)) {
		return false
	}
	a, err := registry.OpenKey(registry.CURRENT_USER, approvedKey, registry.QUERY_VALUE)
	if err != nil {
		return true
	}
	defer a.Close()
	state, _, err := a.GetBinaryValue(Label)
	return err != nil || len(state) == 0 || state[0]&1 == 0
}
