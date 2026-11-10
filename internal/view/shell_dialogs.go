// The shell's dialogs: confirmations, the error detail behind a toast,
// the invalid-config alert and the system-proxy recovery.

package view

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// confirmReq is a pending confirmation dialog.
type confirmReq struct {
	title, body, ok string
	danger          bool
	onOK            func()
}

// detailReq is the full text behind an error toast's 「查看详情」.
type detailReq struct {
	title, text string
}

// ask is Env.Confirm.
func (s *Shell) ask(title, body, okLabel string, danger bool, onOK func()) {
	s.confirm = &confirmReq{title: title, body: body, ok: okLabel, danger: danger, onOK: onOK}
	s.confirmOpen = true
	s.Redraw()
}

// AskQuit shows the quit confirmation; quit runs on the UI thread if the
// user agrees.
func (s *Shell) AskQuit(quit func()) {
	s.ask(i18n.T("退出后将关闭系统代理。"), i18n.T("由澜流开启的系统代理会恢复为关闭，内核也会停止。"), i18n.T("退出"), false, quit)
}

// errorToast shows an error toast with a short first line and 「查看详情」
// opening the full text (docs/design.md §6.5).
func (s *Shell) errorToast(msg, detail string) {
	short, full := splitError(msg)
	if detail != "" {
		full = strings.TrimSpace(msg + "\n\n" + detail)
	}
	s.toast(toastReq{msg: short, isError: true, action: i18n.T("查看详情"), onAction: func() { s.showDetail(full) }})
}

// splitError returns the toast line of an error message and the full text:
// the first line, cut at the first sentence end once it gets long.
func splitError(msg string) (short, full string) {
	full = strings.TrimSpace(msg)
	short = full
	if i := strings.IndexAny(short, "\n\r"); i >= 0 {
		short = strings.TrimSpace(short[:i])
	}
	const limit = 64
	if r := []rune(short); len(r) > limit {
		cut := -1
		for i, c := range r[:limit] {
			if i >= 16 && (c == '。' || c == '：' || c == '，' || c == '；' || c == '.' || c == ':') {
				cut = i
			}
		}
		if cut > 0 {
			short = string(r[:cut]) + "…"
		} else {
			short = string(r[:limit]) + "…"
		}
	}
	return short, full
}

// showDetail opens the detail dialog; safe from any goroutine.
func (s *Shell) showDetail(text string) {
	s.post(func() {
		s.detail = &detailReq{title: i18n.T("错误详情"), text: text}
		s.detailOpen = true
	})
}

// dialogs draws the confirmation, the invalid-config alert and the
// recovery dialog.
func (s *Shell) dialogs(c *ui.Context, st model.Status) {
	t := c.Theme()
	k := s.env.Kit
	if s.confirm != nil {
		cf := s.confirm
		if ui.AlertDialog(c, &s.confirmOpen, cf.title, cf.body, i18n.T("取消"), cf.ok) == 1 {
			s.confirm = nil
			if cf.onOK != nil {
				cf.onOK()
			}
		} else if !s.confirmOpen {
			s.confirm = nil
		}
	}
	if s.detail != nil {
		d := s.detail
		ui.Modal(c, &s.detailOpen, func() {
			ui.Column(c).Width(460).Gap(12).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Box(c).TextColor(t.Danger).Children(func() { k.Icon(c, "warn", 18) })
					ui.Text(c, d.title).FontSize(15).FontWeight(600)
				})
				ui.Scroll(c).MaxHeight(260).Children(func() {
					ui.Text(c, d.text).FontSize(13).Padding(8, 10).Radius(6).
						Background(t.Surface).Border(1, t.Border).Selectable()
				})
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, i18n.T("复制")).Clicked() {
						c.WriteClipboard(d.text)
						c.Toast(i18n.T("已复制"))
					}
					if ui.PrimaryButton(c, i18n.T("好")).Clicked() {
						s.detailOpen = false
					}
				})
			})
		})
		if !s.detailOpen {
			s.detail = nil
		}
	}
	if s.invalid != nil {
		n := s.invalid
		ui.Modal(c, &s.invalidOpen, func() {
			ui.Column(c).Width(440).Gap(12).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Box(c).TextColor(t.Warning).Children(func() { k.Icon(c, "warn", 18) })
					ui.Text(c, firstNonEmpty(n.Title, i18n.T("配置无法使用"))).FontSize(15).FontWeight(600)
				})
				ui.Text(c, s.invalidBody(n)).FontSize(13)
				if n.Detail != "" {
					ui.Text(c, n.Detail).Font("monospace").FontSize(12).Padding(8, 10).Radius(6).
						Background(t.Surface).Border(1, t.Border).Selectable()
				}
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, i18n.T("复制错误")).Clicked() {
						c.WriteClipboard(n.Detail)
						c.Toast(i18n.T("已复制"))
					}
					if ui.PrimaryButton(c, i18n.T("好")).Clicked() {
						s.invalidOpen = false
					}
				})
			})
		})
		if !s.invalidOpen {
			s.invalid = nil
		}
	}
	s.consentDialog(c)
	s.feedbackDialogs(c)
	if s.recovery && !st.PendingRecovery {
		// Answered elsewhere (ctl, a new service process that took over
		// the core): nothing left to decide.
		s.recovery = false
	}
	if s.recovery {
		ui.Modal(c, &s.recovery, func() {
			ui.Column(c).Width(440).Gap(12).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Box(c).TextColor(t.Warning).Children(func() { k.Icon(c, "warn", 18) })
					ui.Text(c, i18n.T("系统代理未恢复")).FontSize(15).FontWeight(600)
				})
				port := st.MixedPort
				if port == 0 {
					port = 7890
				}
				ui.Text(c, i18n.Tf("上次澜流未正常退出，系统代理仍指向 127.0.0.1:%d。如果不处理，部分应用可能无法联网。", port)).FontSize(13)
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, i18n.T("保持开启并启动内核")).Clicked() {
						s.recovery = false
						s.run(func() error { return s.api.ResolveRecovery(false) }, "")
					}
					if ui.PrimaryButton(c, i18n.T("恢复为关闭")).Clicked() {
						s.recovery = false
						s.run(func() error { return s.api.ResolveRecovery(true) }, i18n.T("系统代理已关闭"))
					}
				})
			})
		})
	}
}

// invalidBody words the invalid-config message with the subscription names.
func (s *Shell) invalidBody(n *app.Notice) string {
	var bad, cur string
	for _, sub := range s.api.Subscriptions() {
		if sub.ID == n.Subject {
			bad = sub.Name
		}
		if sub.Current {
			cur = sub.Name
		}
	}
	switch {
	case bad != "" && bad == cur:
		// An update of the current subscription failed validation.
		return i18n.Tf("「%s」更新后的配置未通过校验，已继续使用上次的配置。", bad)
	case bad != "" && cur != "":
		return i18n.Tf("「%s」的配置未通过校验，已继续使用「%s」。", bad, cur)
	case bad != "":
		return i18n.Tf("「%s」的配置未通过校验，当前配置保持不变。", bad)
	default:
		return i18n.T("配置未通过校验，当前配置保持不变。")
	}
}
