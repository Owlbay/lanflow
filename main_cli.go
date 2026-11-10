package main

import (
	"github.com/egoist/mygo"

	"lanflow/internal/ctl"
	"lanflow/internal/mcp"
)

// runSubcommand handles `lanflow ctl …` and `lanflow mcp`: clients of the
// running app's local interface (internal/rpc), started from a terminal
// or by an agent; and `lanflow capture-agent`, the decryption process the
// window starts (main_capture.go). They never open a window or take the single-instance
// lock. ok is false for a normal launch.
func runSubcommand(args []string) (code int, ok bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "ctl":
		return ctl.Main(args[1:], ctl.Env{Version: mygo.App.Version()}), true
	case "mcp":
		return mcp.Main(mygo.App.Version()), true
	case "capture-agent":
		return captureAgentMain(), true // main_capture.go
	}
	return 0, false
}
