package view

import (
	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
)

// Consent for agents (docs/service-split.md §4): when `lanflow mcp` (or
// ctl, for sensitive reads and dangerous actions) needs the user's
// approval, the local interface asks through AskAgentConsent. The dialog
// comes up over the window, raised if hidden, with a system notification
// in case the user is elsewhere. Requests queue; each gets one answer.

// AgentConsent describes one request.
type AgentConsent struct {
	Client  string // "ctl" or "mcp:<name>"
	Agent   string // the MCP client's name; "" for ctl
	Kind    string // "write", "sensitive" or "danger"
	Summary string // the call, parameters redacted
}

type consentReq struct {
	AgentConsent
	answer func(string)
	done   bool
}

// AskAgentConsent queues the dialog; answer is called once, on the UI
// thread, with "once", "always" or "deny". The returned function withdraws
// the request (the caller gave up waiting); answer is not called then.
func (s *Shell) AskAgentConsent(req AgentConsent, answer func(string)) (withdraw func()) {
	r := &consentReq{AgentConsent: req, answer: answer}
	s.post(func() {
		s.consents = append(s.consents, r)
		s.consentOpen = true
	})
	// Window methods are safe from any goroutine (they run on the main
	// thread); the queued dialog draws once the window is up.
	s.ShowWindow()
	if s.notify != nil {
		s.notify(i18n.T("有 agent 请求授权"), consentTitle(req), s.ShowWindow)
	}
	return func() {
		s.post(func() {
			r.done = true
			s.dropConsent(r)
		})
	}
}

func (s *Shell) dropConsent(r *consentReq) {
	for i, q := range s.consents {
		if q == r {
			s.consents = append(s.consents[:i], s.consents[i+1:]...)
			break
		}
	}
	s.consentOpen = len(s.consents) > 0
}

func consentWho(req AgentConsent) string {
	if req.Agent != "" {
		return req.Agent
	}
	return "lanflow ctl"
}

func consentTitle(req AgentConsent) string {
	who := consentWho(req)
	switch req.Kind {
	case "sensitive":
		return i18n.Tf("允许「%s」读取敏感内容吗？", who)
	case "danger":
		return i18n.Tf("允许「%s」执行这项操作吗？", who)
	}
	return i18n.Tf("允许「%s」修改澜流的配置吗？", who)
}

func consentBody(req AgentConsent) string {
	switch req.Kind {
	case "sensitive":
		return i18n.T("它请求读取连接明细、日志原文或订阅内容等敏感信息。允许只对这次连接有效。")
	case "danger":
		return i18n.T("这是一项影响较大的操作，只对这一次请求有效。")
	}
	return i18n.T("它想通过本机接口修改设置、规则、节点或订阅。每次修改都会记入审计日志，可以用 lanflow ctl undo 撤销。选择「始终允许」后，可在 设置 › 高级 › Agent 接入 撤销。")
}

// consentDialog draws the oldest pending request.
func (s *Shell) consentDialog(c *ui.Context) {
	if len(s.consents) == 0 {
		return
	}
	r := s.consents[0]
	t := c.Theme()
	k := s.env.Kit
	reply := func(a string) {
		if r.done {
			return
		}
		r.done = true
		s.dropConsent(r)
		r.answer(a)
	}
	ui.Modal(c, &s.consentOpen, func() {
		ui.Column(c).Width(460).Gap(12).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Box(c).TextColor(t.Warning).Children(func() { k.Icon(c, "warn", 18) })
				ui.Text(c, consentTitle(r.AgentConsent)).FontSize(15).FontWeight(600)
			})
			ui.Text(c, consentBody(r.AgentConsent)).FontSize(13)
			if r.Summary != "" {
				ui.Text(c, r.Summary).Font("monospace").FontSize(12).Padding(8, 10).Radius(6).
					Background(t.Surface).Border(1, t.Border).Selectable()
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, i18n.T("拒绝")).Clicked() {
					reply("deny")
				}
				if r.Kind == "write" {
					if ui.Button(c, i18n.T("仅本次")).Clicked() {
						reply("once")
					}
					if ui.PrimaryButton(c, i18n.T("始终允许")).Clicked() {
						reply("always")
					}
				} else if ui.PrimaryButton(c, i18n.T("允许")).Clicked() {
					reply("once")
				}
			})
		})
	})
	if !s.consentOpen && !r.done {
		// Closed with Esc or a click outside: a refusal.
		reply("deny")
	}
}
