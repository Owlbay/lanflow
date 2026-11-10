package settings

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// 设置 › 关于 › 反馈问题 and 反馈上报 (docs/feedback-api.md): the entry to
// the 反馈问题 dialog, and where reports go — the endpoint, its token in
// the keychain and the optional automatic report. Empty by default: reports
// can then only be saved to a file.

// feedbackDocURL explains the report contract for a self-hosted endpoint.
const feedbackDocURL = "https://github.com/Owlbay/lanflow/blob/main/docs/feedback-api.md"

type feedbackUI struct {
	open         bool
	endpoint     field
	endpointErr  string
	token        string
	tokenSession int
}

func validEndpoint(v string) bool {
	return strings.HasPrefix(v, "https://") || strings.HasPrefix(v, "http://localhost") || strings.HasPrefix(v, "http://127.0.0.1")
}

// feedbackRows are the rows of 关于.
func (p *Page) feedbackRows(c *ui.Context, t *ui.Theme, s model.Settings) {
	e := p.e
	info := e.API.FeedbackInfo()
	e.Kit.Row(c, i18n.T("反馈问题"), i18n.T("描述遇到的问题，附上脱敏后的诊断信息；发送前可以查看全部内容"), func() {
		ui.Button(c, i18n.T("反馈问题…")).Disabled(e.OpenFeedback == nil).OnClick(func() {
			if e.OpenFeedback != nil {
				e.OpenFeedback()
			}
		})
	})
	desc := i18n.T("未设置上报地址：反馈只能保存到文件")
	if info.Endpoint != "" {
		desc = i18n.Tf("发送到 %s", info.Endpoint)
		if info.AutoReport {
			desc += i18n.T(" · 出错时自动上报错误摘要")
		}
	}
	e.Kit.Row(c, i18n.T("反馈上报"), desc, func() {
		if ui.Button(c, i18n.T("配置…")).Clicked() {
			p.fbk.open, p.fbk.endpointErr = true, ""
		}
	})
	p.feedbackConfigDialog(c, t, s, info)
}

func (p *Page) feedbackConfigDialog(c *ui.Context, t *ui.Theme, s model.Settings, info model.FeedbackInfo) {
	e := p.e
	f := &p.fbk
	if !f.open {
		return
	}
	ui.Modal(c, &f.open, func() {
		ui.Column(c.Key("feedback-config")).Width(520).Gap(12).Children(func() {
			ui.Text(c, i18n.T("反馈上报")).FontSize(15).Bold()
			ui.RichText(c).FontSize(12).TextColor(t.TextMuted).Children(func() {
				ui.Text(c, i18n.T("反馈会以 POST {上报地址}/v1/reports 发送到你自己部署的服务（例如 Cloudflare Worker）；接口约定见 "))
				ui.Link(c, "docs/feedback-api.md", feedbackDocURL)
				ui.Text(c, i18n.T("。留空时反馈只能保存到文件，再自行发送。"))
			})
			ui.Column(c).Radius(8).Border(1, t.Border).Clip().Dividers(1, t.Border).Children(func() {
				e.Kit.Row(c, i18n.T("上报地址"), i18n.T("例如 https://lanflow-feedback.example.workers.dev"), func() {
					ui.Column(c).Gap(4).AlignItems(ui.End).Children(func() {
						if f.endpointErr == "" {
							f.endpoint.show(s.Feedback.Endpoint)
						}
						in := ui.TextInput(c.Key("fb-endpoint"), &f.endpoint.text).Width(280).Label(i18n.T("上报地址")).Invalid(f.endpointErr != "")
						if in.Changed() {
							f.endpointErr = ""
						}
						if f.endpoint.commit(in) {
							v := strings.TrimRight(strings.TrimSpace(f.endpoint.text), "/")
							switch {
							case v != "" && !validEndpoint(v):
								f.endpointErr = i18n.T("上报地址须是以 https:// 开头的完整地址")
							case v != s.Feedback.Endpoint:
								s.Feedback.Endpoint = v
								if v == "" {
									s.Feedback.AutoReport = false
								}
								p.save(s, "", nil)
							}
						}
						if f.endpointErr != "" {
							ui.Text(c, f.endpointErr).FontSize(11).TextColor(t.Danger)
						}
					})
				})
				tokDesc := i18n.T("可选；以 Authorization: Bearer 发送，保存在系统钥匙串中")
				if info.TokenSet {
					tokDesc = i18n.T("已设置；保存在系统钥匙串中")
				}
				e.Kit.Row(c, i18n.T("访问令牌"), tokDesc, func() {
					ui.Row(c.Key("fb-token-" + strconv.Itoa(f.tokenSession))).Gap(8).AlignItems(ui.Center).Children(func() {
						ph := i18n.T("输入令牌")
						if info.TokenSet {
							ph = "••••••••"
						}
						in := ui.TextInput(c.Key("fb-token"), &f.token).Width(160).Password().Placeholder(ph).Label(i18n.T("访问令牌"))
						save := func() {
							tok := f.token
							if strings.TrimSpace(tok) == "" {
								return
							}
							f.token = ""
							f.tokenSession++
							e.Run(func() error { return e.API.SetFeedbackToken(tok) }, i18n.T("令牌已保存"))
						}
						if in.Submitted() {
							save()
						}
						ui.Button(c, i18n.T("保存")).Disabled(strings.TrimSpace(f.token) == "").OnClick(save)
						if info.TokenSet {
							ui.Button(c, i18n.T("清除")).OnClick(func() {
								e.Run(func() error { return e.API.SetFeedbackToken("") }, i18n.T("令牌已清除"))
							})
						}
					})
				})
				e.Kit.Row(c, i18n.T("出错时自动上报"), i18n.T("只发送错误摘要（时间、类别、脱敏后的消息与版本），不含诊断信息和日志；默认关闭"), func() {
					v := s.Feedback.AutoReport
					if ui.Switch(c.Key("fb-auto"), &v).Label(i18n.T("出错时自动上报")).Disabled(p.saving || s.Feedback.Endpoint == "").Changed() {
						s.Feedback.AutoReport = v
						p.save(s, "", nil)
					}
				})
			})
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.PrimaryButton(c, i18n.T("完成")).Clicked() {
					f.open = false
				}
			})
		})
	})
}
