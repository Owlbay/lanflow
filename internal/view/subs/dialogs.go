package subs

import (
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// Import phases.
const (
	phaseIdle = iota
	phaseLoading
	phaseDone
	phaseError
)

// importDialog is the state of 从链接导入 (docs/design.md §6.1).
type importDialog struct {
	open     bool
	url      string
	name     string
	use      bool
	advanced bool
	ua       string
	viaProxy bool
	phase    int
	step     int // 0 下载, 1 校验, 2 完成
	err      string
	result   string // the imported subscription's name
	doneAt   time.Time
	// pendingResult is set by the import goroutine, taken by the view.
	pendingResult *importResult
}

// editDialog is the state of 编辑订阅 (docs/design.md §6.2).
type editDialog struct {
	open     bool
	id       string
	name     string
	url      string
	isURL    bool
	auto     int // index into autoLabels
	ua       string
	viaProxy bool
	timeout  string // download timeout in seconds, "" = default
	err      string
	saving   bool
}

// Download timeout bounds (seconds) of the edit dialog.
const (
	defaultTimeoutSec = 30
	maxTimeoutSec     = 600
)

// rawDialog is the state of 查看配置 (docs/design.md §5.3): the stored YAML
// of a subscription, read-only.
type rawDialog struct {
	open    bool
	name    string
	loading bool
	err     string
	text    string
	state   *editor.State
}

var (
	autoLabels = []string{"关闭", "每 6 小时", "每 12 小时", "每 24 小时"}
	autoHours  = []int{0, 6, 12, 24}
	yamlLang   = editor.LanguageFor("config.yaml")
)

// openRaw opens 查看配置 and loads the subscription's YAML.
func (p *Page) openRaw(s model.Subscription) {
	p.mu.Lock()
	p.raw = rawDialog{open: true, name: s.Name, loading: true}
	p.mu.Unlock()
	p.e.Run(func() error {
		text, err := p.e.API.SubscriptionRaw(s.ID)
		p.mu.Lock()
		defer p.mu.Unlock()
		if !p.raw.open || p.raw.name != s.Name {
			return nil // closed, or another one opened meanwhile
		}
		p.raw.loading = false
		if err != nil {
			p.raw.err = err.Error()
			return nil // shown in the dialog
		}
		buf := editor.NewBuffer(text)
		buf.SetLanguage(yamlLang)
		p.raw.text = text
		p.raw.state = &editor.State{Buffer: buf, ReadOnly: true, SoftWrap: true, NoActiveLine: true, KeepScrollbars: true, Label: i18n.T("订阅配置")}
		return nil
	}, "")
}

// rawView draws 查看配置: a read-only monospace YAML viewer with copy and
// the editor's own find bar (⌘F).
func (p *Page) rawView(c *ui.Context) {
	t := c.Theme()
	p.mu.Lock()
	d := p.raw // the loader writes it from another goroutine
	p.mu.Unlock()
	ui.Modal(c, &d.open, func() {
		ui.Column(c.Key("raw")).Width(640).Gap(10).Children(func() {
			ui.Text(c, i18n.T("查看配置 · ")+d.name).FontSize(15).FontWeight(600)
			find := "⌘F"
			if !p.e.Mac {
				find = "Ctrl+F"
			}
			ui.Text(c, i18n.Tf("订阅下载到本地的原始 YAML（只读，未应用覆写）。%s 查找。", find)).FontSize(12).TextColor(t.TextMuted)
			switch {
			case d.loading:
				ui.Row(c).Gap(6).Height(380).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
					ui.Spinner(c)
					ui.Text(c, i18n.T("正在读取…")).TextColor(t.TextMuted)
				})
			case d.err != "":
				ui.Text(c, d.err).TextColor(t.Danger).Role(ui.RoleAlert).Height(380)
			default:
				editor.View(c, d.state).Height(380).FillWidth().Radius(6).Border(1, t.Border).AutoFocus()
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Button(c, i18n.T("复制")).Disabled(d.text == "").OnClick(func() {
					c.WriteClipboard(d.text)
					p.e.Toast(i18n.T("已复制"), false)
				})
				ui.Spacer(c)
				if ui.PrimaryButton(c, i18n.T("关闭")).Clicked() {
					d.open = false
				}
			})
		})
	})
	if !d.open {
		p.mu.Lock()
		p.raw.open = false
		p.mu.Unlock()
	}
}

// autoIndex maps a stored interval to the nearest offered choice, so a
// value the server set (e.g. profile-update-interval: 8) shows as 每 6 小时
// rather than 关闭, and saving the dialog keeps auto-update on
// (docs/design.md:345, §6.2). Only ≤ 0 means 关闭; ties round down.
func autoIndex(hours int) int {
	if hours <= 0 {
		return 0
	}
	diff := func(i int) int {
		d := autoHours[i] - hours
		if d < 0 {
			d = -d
		}
		return d
	}
	best := 1
	for i := 2; i < len(autoHours); i++ {
		if diff(i) < diff(best) {
			best = i
		}
	}
	return best
}

// openImport opens the URL import dialog, prefilled from the clipboard
// when it holds a link.
func (p *Page) openImport(c *ui.Context) {
	p.imp = importDialog{open: true, use: true}
	if clip := strings.TrimSpace(c.ReadClipboard()); isURL(clip) {
		p.imp.url = clip
	}
}

// dialogs draws the import, edit and 查看配置 dialogs.
func (p *Page) dialogs(c *ui.Context) {
	p.finishImport(c)
	if p.imp.open {
		switch p.imp.phase {
		case phaseLoading:
			// The service reports where the import is (下载 → 校验).
			p.imp.step = 0
			if p.e.API.Status().ImportPhase == model.ImportValidating {
				p.imp.step = 1
			}
		case phaseDone:
			if left := 400*time.Millisecond - c.Now().Sub(p.imp.doneAt); left > 0 {
				c.After(left)
			} else {
				p.imp.open = false
			}
		}
	}
	if p.imp.open {
		p.importView(c)
	}
	if p.edit.open {
		p.editView(c)
	}
	if p.raw.open {
		p.rawView(c)
	}
}

func (p *Page) importView(c *ui.Context) {
	t := c.Theme()
	d := &p.imp
	loading := d.phase == phaseLoading || d.phase == phaseDone
	md := ui.Modal(c, &d.open, func() {
		ui.Column(c).Width(420).Gap(10).Children(func() {
			ui.Text(c, i18n.T("从链接导入订阅")).FontSize(15).FontWeight(600)
			field(c, i18n.T("订阅链接"), func() {
				in := ui.TextInput(c.Key("url"), &d.url).Placeholder("https://").Label(i18n.T("订阅链接")).AutoFocus().ReadOnly(loading).Invalid(d.phase == phaseError)
				if in.Submitted() {
					p.doImport()
				}
			})
			field(c, i18n.T("名称（可选）"), func() {
				ui.TextInput(c.Key("name"), &d.name).Placeholder(i18n.T("留空则使用订阅提供的名称")).Label(i18n.T("订阅名称")).ReadOnly(loading)
			})
			ui.Checkbox(c, &d.use, i18n.T("导入后立即使用")).Disabled(loading)
			ui.Collapsible(c, i18n.T("高级：User-Agent、通过代理下载"), &d.advanced, func() {
				ui.Column(c).Gap(8).Padding(4, 0, 0, 0).Children(func() {
					field(c, "User-Agent", func() {
						ui.TextInput(c.Key("ua"), &d.ua).Placeholder(i18n.T("留空使用默认 User-Agent")).Label("User-Agent").ReadOnly(loading)
					})
					ui.Checkbox(c, &d.viaProxy, i18n.T("通过代理下载")).Disabled(loading)
				})
			})
			if loading {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					for i, s := range []string{i18n.T("下载"), i18n.T("校验"), i18n.T("完成")} {
						if i > 0 {
							ui.Text(c, "→").FontSize(12).TextColor(t.TextMuted)
						}
						txt, col := s, t.TextMuted
						switch {
						case i < d.step:
							txt, col = "✓ "+s, t.Success
						case i == d.step:
							col = t.Text
						}
						ui.Text(c, txt).FontSize(12).TextColor(col)
					}
				})
			}
			if d.phase == phaseError && d.err != "" {
				ui.Text(c, d.err).FontSize(12).TextColor(t.Danger).Role(ui.RoleAlert)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Margin(6, 0, 0, 0).Children(func() {
				if ui.Button(c, i18n.T("取消")).Clicked() {
					d.open = false
				}
				b := ui.PrimaryButton(c, "").Label(i18n.T("开始导入")).Disabled(loading)
				b.Children(func() {
					if loading {
						ui.Spinner(c).Size(12, 12)
						ui.Text(c, []string{i18n.T("正在下载…"), i18n.T("正在校验…"), i18n.T("完成")}[min(d.step, 2)]).SingleLine()
					} else {
						ui.Text(c, i18n.T("导入")).SingleLine()
					}
				})
				if b.Clicked() {
					p.doImport()
				}
			})
		})
	})
	if md.Dismissed() && loading {
		d.open = true // stay open while the import runs
	}
}

func field(c *ui.Context, label string, control func()) {
	t := c.Theme()
	ui.Column(c).Gap(4).Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(t.TextMuted)
		control()
	})
}

// doImport validates the link, then downloads and validates through the API.
func (p *Page) doImport() {
	d := &p.imp
	if d.phase == phaseLoading {
		return
	}
	url := strings.TrimSpace(d.url)
	if !isURL(url) {
		d.phase, d.err = phaseError, i18n.T("请输入以 http:// 或 https:// 开头的订阅链接。")
		return
	}
	d.phase, d.step, d.err = phaseLoading, 0, ""
	name, ua, viaProxy, use := strings.TrimSpace(d.name), strings.TrimSpace(d.ua), d.viaProxy, d.use
	first := !p.e.API.Status().HasSubscriptions
	p.async(func() {
		sub, err := p.e.API.ImportSubscriptionURL(url, name, ua, viaProxy, use)
		var startErr error
		if err == nil && first {
			startErr = p.e.API.StartCore()
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		p.imp.pendingResult = &importResult{sub: sub, err: err, startErr: startErr, first: first, use: use}
		p.e.Redraw()
	})
}

// importResult is what the import goroutine hands back to the view.
type importResult struct {
	sub      model.Subscription
	err      error
	startErr error
	first    bool
	use      bool
}

// finishImport applies the import result on the UI thread.
func (p *Page) finishImport(c *ui.Context) {
	p.mu.Lock()
	r := p.imp.pendingResult
	p.imp.pendingResult = nil
	p.mu.Unlock()
	if r == nil {
		return
	}
	d := &p.imp
	if r.err != nil {
		d.phase, d.err = phaseError, r.err.Error()
		return
	}
	d.phase, d.step, d.result, d.doneAt = phaseDone, 2, r.sub.Name, c.Now()
	c.After(400 * time.Millisecond)
	switch {
	case r.startErr != nil:
		p.e.Toast(i18n.Tf("已导入 %s，但内核启动失败：%s", r.sub.Name, r.startErr.Error()), true)
	case r.first:
		p.e.Toast(i18n.T("已导入并启动"), false)
	case r.use:
		p.e.Toast(i18n.T("已导入并切换到 ")+r.sub.Name, false)
	default:
		p.e.Toast(i18n.T("订阅已导入"), false)
	}
}

func (p *Page) editView(c *ui.Context) {
	t := c.Theme()
	d := &p.edit
	ui.Modal(c, &d.open, func() {
		ui.Column(c).Width(420).Gap(10).Children(func() {
			ui.Text(c, i18n.T("编辑订阅")).FontSize(15).FontWeight(600)
			field(c, i18n.T("名称"), func() {
				ui.TextInput(c.Key("name"), &d.name).Label(i18n.T("名称")).AutoFocus().Invalid(d.err != "" && strings.TrimSpace(d.name) == "")
			})
			if d.isURL {
				field(c, i18n.T("链接"), func() {
					ui.TextInput(c.Key("url"), &d.url).Label(i18n.T("链接")).Invalid(d.err != "" && !isURL(d.url))
				})
				field(c, i18n.T("自动更新"), func() {
					ui.Segmented(c, &d.auto, i18n.Options(autoLabels...)...).Label(i18n.T("自动更新"))
				})
				field(c, "User-Agent", func() {
					ui.TextInput(c.Key("ua"), &d.ua).Placeholder(i18n.T("留空使用默认 User-Agent")).Label("User-Agent")
				})
				ui.Checkbox(c, &d.viaProxy, i18n.T("通过代理更新"))
				field(c, i18n.T("下载超时（秒）"), func() {
					ui.TextInput(c.Key("timeout"), &d.timeout).Placeholder(i18n.Tf("留空为 %d 秒", defaultTimeoutSec)).Width(120).FontFeatures("tnum").
						Label(i18n.T("下载超时（秒）")).Invalid(d.err != "" && !validTimeout(d.timeout))
				})
			}
			if d.err != "" {
				ui.Text(c, d.err).FontSize(12).TextColor(t.Danger).Role(ui.RoleAlert)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Margin(6, 0, 0, 0).Children(func() {
				if ui.Button(c, i18n.T("取消")).Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, i18n.T("保存")).Disabled(d.saving).Clicked() || c.Shortcut(0, ui.KeyEnter) {
					p.saveEdit()
				}
			})
		})
	})
}

// saveEdit validates and saves the edited subscription.
func (p *Page) saveEdit() {
	d := &p.edit
	if strings.TrimSpace(d.name) == "" {
		d.err = i18n.T("请填写名称。")
		return
	}
	if d.isURL && !isURL(d.url) {
		d.err = i18n.T("请输入以 http:// 或 https:// 开头的订阅链接。")
		return
	}
	if d.isURL && !validTimeout(d.timeout) {
		d.err = i18n.Tf("下载超时须为 1–%d 之间的秒数，或留空。", maxTimeoutSec)
		return
	}
	var cur model.Subscription
	for _, s := range p.e.API.Subscriptions() {
		if s.ID == d.id {
			cur = s
		}
	}
	cur.Name, cur.UserAgent, cur.ViaProxy = strings.TrimSpace(d.name), strings.TrimSpace(d.ua), d.viaProxy
	cur.AutoUpdateHours = autoHours[min(d.auto, len(autoHours)-1)]
	if d.isURL {
		cur.URL = strings.TrimSpace(d.url)
		cur.TimeoutSec = timeoutSec(d.timeout)
	}
	d.open, d.err = false, ""
	p.e.Run(func() error { return p.e.API.EditSubscription(cur) }, i18n.T("已保存"))
}

// timeoutText is the edit dialog's text of a stored timeout ("" = default).
func timeoutText(sec int) string {
	if sec <= 0 {
		return ""
	}
	return strconv.Itoa(sec)
}

// timeoutSec parses the dialog's text; "" means the default (0).
func timeoutSec(text string) int {
	n, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil {
		return 0
	}
	return n
}

// validTimeout accepts an empty text or 1–maxTimeoutSec.
func validTimeout(text string) bool {
	if strings.TrimSpace(text) == "" {
		return true
	}
	n, err := strconv.Atoi(strings.TrimSpace(text))
	return err == nil && n >= 1 && n <= maxTimeoutSec
}
