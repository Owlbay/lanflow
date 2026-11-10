package settings

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestMCPConfig(t *testing.T) {
	exe := "/Applications/Lanflow.app/Contents/MacOS/Lanflow"
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(MCPConfig(exe)), &cfg); err != nil {
		t.Fatal(err)
	}
	s := cfg.MCPServers["lanflow"]
	if s.Command != exe || len(s.Args) != 1 || s.Args[0] != "mcp" {
		t.Fatalf("config = %+v", cfg)
	}
	if runtime.GOOS != "windows" {
		if got := MCPCommand("/Apps/My Lanflow/Lanflow"); !strings.HasSuffix(got, "-- '/Apps/My Lanflow/Lanflow' mcp") {
			t.Fatalf("command = %s", got)
		}
	}
}
