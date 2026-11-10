// Package rpc is Lanflow's local interface (docs/service-split.md §3–§5):
// a JSON-RPC 2.0 server, one message per line, over a unix socket
// (<DataDir>/run/lanflow.sock) or a per-user named pipe on Windows, that
// serves app.API to the window, `lanflow ctl` and `lanflow mcp`.
//
// The method names are those of app.API; parameters travel as a JSON array
// in declaration order and results as a JSON array of the non-error return
// values. A few methods exist only here (hello, subscribe, PatchUIState and
// the agent methods in agentops.go). Every call from ctl and MCP passes the
// permission levels of §4 (guard.go), and their writes are audited and can
// be undone (audit.go).
package rpc

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ProtocolVersion changes whenever the wire format or the meaning of a
// method changes; client and server must agree (no cross-version support).
const ProtocolVersion = 1

// Level is how much a method may change or reveal (docs/service-split.md §4).
type Level string

const (
	LevelRead      Level = "read"      // status, lists, redacted settings, measurements
	LevelSensitive Level = "sensitive" // subscription contents, connection details, raw logs
	LevelWrite     Level = "write"     // configuration changes
	LevelDanger    Level = "danger"    // helper, core binary, backups, deleting subscriptions
	LevelUI        Level = "ui"        // only the window calls these (notice queue, UI state)
)

// Param is one parameter of a method.
type Param struct {
	Name string `json:"name"`
	Type string `json:"type"` // Go type as written in app.API
}

// Method is one entry of the manifest.
type Method struct {
	Name    string   `json:"name"`
	Params  []Param  `json:"params,omitempty"`
	Results []string `json:"results,omitempty"` // Go types, error excluded
	Error   bool     `json:"error,omitempty"`   // the last result is an error
	Level   Level    `json:"level"`
	Doc     string   `json:"doc,omitempty"`
	// Agent marks the methods that exist only on the local interface
	// (agentops.go), not in app.API.
	Agent bool `json:"agent,omitempty"`
}

// Client kinds, declared in the handshake.
const (
	KindUI  = "ui"
	KindCtl = "ctl"
	KindMCP = "mcp"
)

// Identity is who a connection says it is: "ui", "ctl" or "mcp:<name>".
type Identity struct {
	Kind string
	Name string // the MCP client's name; "" for ui and ctl
}

func (id Identity) String() string {
	if id.Kind == KindMCP {
		return KindMCP + ":" + id.Name
	}
	return id.Kind
}

// Agent reports whether permission checks apply (everything but the window).
func (id Identity) Agent() bool { return id.Kind != KindUI }

// ParseIdentity parses the handshake's client field.
func ParseIdentity(s string) (Identity, error) {
	kind, name, _ := strings.Cut(strings.TrimSpace(s), ":")
	switch kind {
	case KindUI, KindCtl:
		if name != "" {
			return Identity{}, fmt.Errorf("客户端身份无效: %q", s)
		}
		return Identity{Kind: kind}, nil
	case KindMCP:
		name = strings.TrimSpace(name)
		if name == "" {
			name = "unknown"
		}
		if len(name) > 64 || strings.ContainsAny(name, "\r\n\t") {
			return Identity{}, fmt.Errorf("客户端名称无效: %q", name)
		}
		return Identity{Kind: KindMCP, Name: name}, nil
	}
	return Identity{}, fmt.Errorf("客户端身份无效: %q（应为 ui、ctl 或 mcp:<名称>）", s)
}

// ---- wire format ----

// maxLine bounds one message (connection lists can be large).
const maxLine = 64 << 20

// message is any JSON-RPC 2.0 message: request (ID and Method), response
// (ID and Result or Error) or notification (Method without ID).
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error codes. The -32000 range is the server's own.
const (
	CodeParse          = -32700
	CodeInvalidRequest = -32600
	CodeNoMethod       = -32601
	CodeInvalidParams  = -32602
	CodeApp            = -32000 // the method returned an error; Message is the user-facing text
	CodeDenied         = -32001 // the permission level forbids the call for this client
	CodeConsent        = -32002 // the user declined, or could not be asked
	CodeHandshake      = -32003 // hello missing or protocol mismatch
)

// Error is a JSON-RPC error object; it is also the error the client
// returns, so callers can test Code.
type Error struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *ErrorData `json:"data,omitempty"`
}

// ErrorData names well-known errors so the client can map them back.
type ErrorData struct {
	Kind string `json:"kind,omitempty"` // e.g. "helper-required"
	// PID is the serving process, on a refused handshake: a window of
	// another protocol version ends that service to start its own.
	PID int `json:"pid,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// Hello is the handshake request, the first message of a connection.
type Hello struct {
	Protocol int    `json:"protocol"`
	Version  string `json:"version"` // the client's Lanflow version
	Client   string `json:"client"`  // ui, ctl or mcp:<name>
	// Reveal asks for sensitive reads (ctl --reveal); the user still
	// confirms them in the window.
	Reveal bool `json:"reveal,omitempty"`
}

// Welcome is the handshake answer.
type Welcome struct {
	Protocol int    `json:"protocol"`
	Version  string `json:"version"` // the server's Lanflow version
	Client   string `json:"client"`  // the identity as understood
	// PID is the serving process, for restarting a service that no longer
	// answers (docs/service-split.md S3); 0 from older versions.
	PID int `json:"pid,omitempty"`
}

// UICommand is the params of the "ui" notification: the service process
// asks the window process to act (docs/service-split.md S2, windowops.go).
type UICommand struct {
	Cmd string `json:"cmd"` // UIShow, UIToggle, UIAskQuit, UIQuit
	Arg string `json:"arg,omitempty"`
}

// UI commands.
const (
	UIShow    = "show"     // bring the window up; Arg: "" or what to open (OpenArg)
	UIToggle  = "toggle"   // the window shortcut: close the window when it is in front, else show it
	UIAskQuit = "ask-quit" // the tray's 退出澜流 while the system proxy is on: confirm in the window
	UIQuit    = "quit"     // the service is quitting: close the window without asking
)

// Changed is the params of the "changed" notification.
type Changed struct {
	Topic string `json:"topic"`
}
