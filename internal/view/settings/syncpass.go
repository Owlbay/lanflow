package settings

import (
	"errors"
	"strconv"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/syncfile"
)

// 用口令加密链接 (docs/config-sync.md), in 设置 › 常规 › 配置同步: the
// switch that seals the sync file's links and WebDAV password with a
// passphrase, changing it, forgetting it on this machine, and the dialog
// that sets, changes or enters it. The passphrase is typed here only:
// the API methods are window-only, an agent can neither set nor enter it.

// passMode is what the passphrase dialog does.
type passMode int

const (
	passSet    passMode = iota // turn sealing on
	passChange                 // a new passphrase
	passUnlock                 // enter it on this device
)

// passDialog is the passphrase dialog's state (under p.mu while a job runs).
type passDialog struct {
	mode          passMode
	open          bool
	pass, confirm string
	err           string // shown in the dialog: a wrong passphrase keeps it open
	busy          bool
	session       int    // bumps the inputs' keys so they start empty
	done          string // a toast to show on the next frame (from the job)
}

func (p *Page) openPassDialog(mode passMode) {
	p.mu.Lock()
	n := 0
	if p.sync.pass != nil {
		n = p.sync.pass.session + 1
	}
	p.sync.pass = &passDialog{mode: mode, open: true, session: n}
	p.mu.Unlock()
}

// syncPassphraseRows draws the 用口令加密链接 row and its actions.
func (p *Page) syncPassphraseRows(c *ui.Context, st model.ConfigSyncStatus, busy bool) {
	e := p.e
	desc := i18n.T("把订阅链接和 WebDAV 密码用口令加密后写进同步文件，其他设备（包括 Windows）输入同一口令即可取回。口令本身不保存；丢失口令后，文件中的链接无法恢复，只能在各设备上重新输入。")
	if !st.Encrypted {
		desc = i18n.T("关闭时同步文件只写钥匙串引用，另一台设备需要重新输入订阅链接。开启后，链接和 WebDAV 密码用口令加密写进文件；口令丢失后无法恢复这些链接。")
	}
	e.Kit.Row(c, i18n.T("用口令加密链接"), desc, func() {
		on := st.Encrypted
		if ui.Switch(c.Key("sync-seal"), &on).Label(i18n.T("用口令加密链接")).Disabled(busy).Changed() {
			if on {
				p.openPassDialog(passSet)
			} else {
				e.Confirm(i18n.T("关闭口令加密？"),
					i18n.T("同步文件改回只写钥匙串引用，加密的链接和 WebDAV 密码从文件中移除，本机钥匙串里的派生密钥也会删除。其他设备之后需要自己的钥匙串里有链接，否则要重新输入。"),
					i18n.T("关闭加密"), true, func() {
						p.syncJob(func() error { return e.API.SetSyncPassphrase("") }, i18n.T("已关闭口令加密；同步文件改回钥匙串引用"))
					})
			}
		}
	})
	if !st.Encrypted || st.Locked {
		return
	}
	e.Kit.Row(c, i18n.T("同步口令"), i18n.T("已在本机保存由口令派生的密钥，启动时无需再次输入。修改口令后，其他设备需要输入新口令。"), func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Button(c, i18n.T("修改口令…")).Disabled(busy).OnClick(func() { p.openPassDialog(passChange) })
			ui.Button(c, i18n.T("在本机忘记口令")).Disabled(busy).OnClick(func() {
				e.Confirm(i18n.T("在本机忘记同步口令？"),
					i18n.T("本机钥匙串中由口令派生的密钥会被删除。本机已有的订阅链接保留；之后同步文件中新增或修改的链接，需要重新输入口令才能取回。"),
					i18n.T("忘记口令"), false, func() {
						p.syncJob(func() error { return e.API.ForgetSyncPassphrase() }, i18n.T("已在本机忘记同步口令"))
					})
			})
		})
	})
}

// submitPass runs the dialog's action off the UI thread (the key
// derivation takes a moment); errors stay in the dialog.
func (p *Page) submitPass() {
	e := p.e
	p.mu.Lock()
	d := p.sync.pass
	if d == nil || d.busy {
		p.mu.Unlock()
		return
	}
	mode, pass, confirm := d.mode, d.pass, d.confirm
	switch {
	case pass == "":
		d.err = i18n.T("请输入同步口令。")
	case mode != passUnlock && utf8.RuneCountInString(pass) < syncfile.MinPassphraseLen:
		d.err = syncfile.ErrShortPassphrase.Error()
	case mode != passUnlock && pass != confirm:
		d.err = i18n.T("两次输入的口令不一致。")
	default:
		d.err, d.busy = "", true
	}
	busy := d.busy
	p.mu.Unlock()
	if !busy {
		return
	}
	e.Run(func() error {
		var err error
		ok := ""
		if mode == passUnlock {
			err = e.API.UnlockSync(pass)
			ok = i18n.T("同步口令正确，已解锁订阅链接")
		} else {
			err = e.API.SetSyncPassphrase(pass)
			ok = i18n.T("已用口令加密同步文件中的链接")
			if mode == passChange {
				ok = i18n.T("同步口令已修改；其他设备需要输入新口令")
			}
		}
		p.mu.Lock()
		if d := p.sync.pass; d != nil {
			d.busy = false
			switch {
			case errors.Is(err, syncfile.ErrWrongPassphrase):
				d.err = i18n.T("同步口令不正确，请重新输入。")
				d.pass = ""
				d.session++
			case err != nil:
				d.err = err.Error()
			default:
				d.open, d.pass, d.confirm, d.done = false, "", "", ok
			}
		}
		p.mu.Unlock()
		e.Redraw()
		return nil
	}, "")
}

// passphraseDialog draws the dialog that sets, changes or enters the
// sync passphrase.
func (p *Page) passphraseDialog(c *ui.Context, t *ui.Theme) {
	p.mu.Lock()
	d := p.sync.pass
	if d != nil && !d.open && d.done != "" {
		done := d.done
		d.done = ""
		p.mu.Unlock()
		p.e.Toast(done, false)
		p.mu.Lock()
	}
	if d != nil && !d.open && !d.busy {
		p.sync.pass = nil
		d = nil
	}
	var busy bool
	var errText string
	var session int
	if d != nil {
		busy, errText, session = d.busy, d.err, d.session
	}
	p.mu.Unlock()
	if d == nil {
		return
	}
	title, intro, okLabel := i18n.T("设置同步口令"), i18n.T("订阅链接和 WebDAV 密码会用这个口令加密后写进同步文件。其他设备开启同步后输入同一口令即可取回。"), i18n.T("加密")
	switch d.mode {
	case passChange:
		title, intro, okLabel = i18n.T("修改同步口令"), i18n.T("所有链接会用新口令重新加密并写回同步文件；其他设备读到新文件后，需要输入新口令。"), i18n.T("修改口令")
	case passUnlock:
		title, intro, okLabel = i18n.T("输入同步口令"), i18n.T("输入在其他设备上设置的同步口令，以解锁同步文件中的订阅链接。口令正确后，链接存入本机钥匙串，之后启动无需再次输入。"), i18n.T("解锁")
	}
	ui.Modal(c, &d.open, func() {
		ui.Column(c.Key("sync-pass")).Width(440).Gap(12).Children(func() {
			ui.Text(c, title).FontSize(15).Bold()
			ui.Text(c, intro).FontSize(12).TextColor(t.TextMuted)
			if d.mode != passUnlock {
				ui.Text(c, i18n.T("口令不会保存在任何地方，澜流也无法找回。口令丢失后，同步文件中的链接无法恢复，只能在各设备上重新输入。同步文件放在第三方云盘上，请使用足够长、不与其他服务共用的口令（至少 8 个字符）。")).FontSize(12).TextColor(t.Danger)
			}
			key := func(k string) *ui.Context { return c.Key(k + "-" + strconv.Itoa(session)) }
			in := ui.TextInput(key("sync-pass-1"), &d.pass).Password().Disabled(busy).Placeholder(i18n.T("同步口令")).Label(i18n.T("同步口令"))
			submitted := in.Submitted()
			if d.mode != passUnlock {
				in2 := ui.TextInput(key("sync-pass-2"), &d.confirm).Password().Disabled(busy).Placeholder(i18n.T("再次输入口令")).Label(i18n.T("再次输入口令"))
				submitted = submitted || in2.Submitted()
			}
			if errText != "" {
				ui.Text(c, errText).FontSize(12).TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if busy {
					ui.Spinner(c).Label(i18n.T("正在处理")).Size(14, 14)
				}
				ui.Spacer(c)
				if ui.Button(c, i18n.T("取消")).Disabled(busy).Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, okLabel).Disabled(busy || d.pass == "").Clicked() || submitted {
					p.submitPass() // the work runs in e.Run, off the UI thread
				}
			})
		})
	})
}
