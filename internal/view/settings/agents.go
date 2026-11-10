package settings

import (
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
