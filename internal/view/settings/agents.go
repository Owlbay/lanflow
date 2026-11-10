package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// Agent 接入 (docs/service-split.md §4–§5), at the end of the 高级 tab:
// the standing answers given to agents that asked to change the
// configuration through `lanflow mcp`, each revocable.

func (p *Page) agentSection(c *ui.Context, s model.Settings) {
	e := p.e
	e.Kit.Section(c, i18n.T("Agent 接入"), i18n.T("Claude 等 agent 可以通过 lanflow mcp 或 lanflow ctl 查看和管理澜流；修改配置前会先请你授权，并记入审计日志"), func() {
		e.Kit.Row(c, i18n.T("MCP 配置"), i18n.T("复制后粘贴到 Claude Desktop 的 claude_desktop_config.json 或项目的 .mcp.json；也可以复制 claude mcp add 命令在终端运行"), func() {
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c.Key("mcp-copy-cmd"), i18n.T("复制命令")).Clicked() {
					c.WriteClipboard(MCPCommand(executable()))
					c.Toast(i18n.T("已复制"))
				}
				if ui.PrimaryButton(c.Key("mcp-copy-json"), i18n.T("复制 MCP 配置")).Clicked() {
					c.WriteClipboard(MCPConfig(executable()))
					c.Toast(i18n.T("已复制"))
				}
			})
		})
		if len(s.AgentGrants) == 0 {
			e.Kit.Row(c, i18n.T("已授权的 agent"), i18n.T("还没有 agent 请求过修改配置"), func() {})
			return
		}
		for _, g := range s.AgentGrants {
			desc := i18n.Tf("始终允许修改配置 · %s", g.At.Local().Format("2006-01-02 15:04"))
			if !g.Allow {
				desc = i18n.Tf("已拒绝修改配置 · %s", g.At.Local().Format("2006-01-02 15:04"))
			}
			e.Kit.Row(c, g.Agent, desc, func() {
				if ui.Button(c.Key("agent-revoke-"+g.Agent), i18n.T("撤销")).Clicked() {
					p.revokeAgent(g.Agent)
				}
			})
		}
	})
}

// revokeAgent forgets the answer for agent: its next write asks again.
func (p *Page) revokeAgent(agent string) {
	s := p.current()
	kept := make([]model.AgentGrant, 0, len(s.AgentGrants))
	for _, g := range s.AgentGrants {
		if g.Agent != agent {
			kept = append(kept, g)
		}
	}
	s.AgentGrants = kept
	p.save(s, i18n.Tf("已撤销「%s」的授权", agent), nil)
}

// executable is the installed app's executable, which `lanflow mcp` runs
// (inside the bundle on macOS: Lanflow.app/Contents/MacOS/Lanflow).
func executable() string {
	exe, err := os.Executable()
	if err != nil {
		return "lanflow"
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return exe
}

// MCPConfig is the mcpServers entry for exe, as Claude Desktop
// (claude_desktop_config.json) and Claude Code (.mcp.json) read it.
func MCPConfig(exe string) string {
	cfg := map[string]any{"mcpServers": map[string]any{"lanflow": map[string]any{"command": exe, "args": []string{"mcp"}}}}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	return string(b)
}

// MCPCommand is the Claude Code command that registers the server.
func MCPCommand(exe string) string {
	q := exe
	if runtime.GOOS == "windows" {
		if strings.Contains(q, " ") {
			q = `"` + q + `"`
		}
	} else if strings.ContainsAny(q, " '\"$") {
		q = "'" + strings.ReplaceAll(q, "'", `'\''`) + "'"
	}
	return "claude mcp add --scope user lanflow -- " + q + " mcp"
}
