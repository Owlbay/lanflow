// Package capture is the 抓包 page (docs/capture.md §7.2): the switch with
// its timer, the root certificate, the selection, the list of requests and
// a read-only detail (headers, bodies, TLS, timing), masked by default.
//
// Like Surge's 最近请求, the list shows every connection the core sees,
// decryption on or off; HTTPS content is only visible for the hosts and
// programs chosen for decryption, which is done from a row (解密此主机 /
// 解密此程序). Decrypted requests replace their connection in the list.
package capture

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/capture/redact"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/procicon"
)

const detailWidth = 400

// Page holds page-local UI state.
type Page struct {
	e *env.Env

	query    string
	onlyDec  bool
	sort     ui.SortOrder
	list     ui.ListState
	selected int
	sel      int64 // selected row: a record ID, or a connection's negative ID
	tab      int   // detail tab
	reveal   bool  // show masked values (never remembered)
	icons    *procicon.Cache

	// connections shown as rows get stable negative IDs
	connIDs  map[string]int64
	nextConn int64
	conns    map[int64]model.Connection

	det struct {
		sync.Mutex
		id      int64
		d       model.CaptureFlowDetail
		err     string
		loading bool
		img     ui.ImageSource
		imgFor  int64
	}

	// the selection editor
	editing bool
	domains string
	bypass  string
	autoOff string
	ports   string
	apps    []model.CaptureApp
	off     []string // built-in entries turned off
	ack     bool
}

// New returns the page.
func New(e *env.Env) *Page {
	p := &Page{e: e, selected: -1, sort: ui.SortOrder{Column: "time", Descending: true}, icons: procicon.New(e.API)}
	p.list.Sort = &p.sort
	p.list.Selected = &p.selected
	p.list.KeepOffset = true
	return p
}

// Topics are the service topics the view reads.
func (p *Page) Topics() []app.Topic {
	return []app.Topic{app.TopicStatus, app.TopicCapture, app.TopicConns, app.TopicProcIcons}
}

var columns = []ui.TableColumn{
	{Title: "时间", ID: "time", Width: 84, Sortable: true},
	{Title: "程序", ID: "process", Width: 110, Sortable: true},
	{Title: "方法", ID: "method", Width: 58, Sortable: true},
	{Title: "状态", ID: "status", Width: 50, Align: ui.End, Sortable: true},
	{Title: "主机", ID: "host", MinWidth: 140, Sortable: true},
	{Title: "路径", ID: "path", MinWidth: 140, Sortable: true},
	{Title: "大小", ID: "size", Width: 70, Align: ui.End, Sortable: true},
	{Title: "耗时", ID: "dur", Width: 62, Align: ui.End, Sortable: true},
	{Title: "标记", ID: "flags", Width: 80},
}

// Remaining formats the time left before the automatic turn-off.
func Remaining(until time.Time) string {
	if until.IsZero() {
		return ""
	}
	d := time.Until(until)
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%02d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

// StatusText is the one-line state used by the page, the sidebar and the
// tray: "解密中 · 2 个主机 · 1 个程序 · 剩余 12:34".
func StatusText(st model.CaptureStatus) string {
	if !st.Enabled {
		return i18n.T("解密已关闭")
	}
	parts := []string{i18n.T("解密中")}
	if st.Paused {
		parts[0] = i18n.Tf("已暂停（%s）", st.PauseReason)
	}
	if st.Domains > 0 {
		parts = append(parts, i18n.Tf("%d 个主机", st.Domains))
	}
	if st.Apps > 0 {
		parts = append(parts, i18n.Tf("%d 个程序", st.Apps))
	}
	if r := Remaining(st.Until); r != "" {
		parts = append(parts, i18n.Tf("剩余 %s", r))
	}
	return strings.Join(parts, " · ")
}

// View draws the page.
func (p *Page) View(c *ui.Context) {
	e, t := p.e, c.Theme()
	st := e.API.CaptureStatus()
	if a := e.Arg(env.Capture); a != "" {
		p.handleArg(a, st)
		st = e.API.CaptureStatus()
	}
	ui.Column(c).Fill().Children(func() {
		e.Kit.Header(c, i18n.T("抓包"), StatusText(st), func() { p.tools(c, st) })
		ui.Column(c).Padding(0, 16).Gap(8).Shrink(0).Children(func() { p.banners(c, st) })
		if p.editing {
			ui.Scroll(c).Grow(1).Padding(8, 16, 16, 16).Gap(14).Children(func() {
				if !st.Settings.Acknowledged {
					p.intro(c, t, st)
				}
				p.editor(c, t, st)
			})
			return
		}
		p.body(c, t, st)
	})
}

func (p *Page) tools(c *ui.Context, st model.CaptureStatus) {
	e := p.e
	ui.SearchField(c.Key("search"), &p.query).Placeholder(i18n.T("搜索")).Label(i18n.T("搜索请求")).Width(150)
	if st.Enabled {
		if !st.Until.IsZero() {
			ui.Button(c, i18n.T("延长 30 分钟")).OnClick(func() { e.Run(e.API.ExtendCapture, "") })
		}
		ui.Button(c, i18n.T("关闭解密")).OnClick(func() { e.Run(func() error { return e.API.SetCapture(false) }, i18n.T("已关闭解密")) })
	} else {
		ui.PrimaryButton(c, i18n.T("开启解密")).Disabled(st.CA != model.CATrusted || (st.Domains == 0 && st.Apps == 0) || !st.Settings.Acknowledged).
			OnClick(func() { e.Run(func() error { return e.API.SetCapture(true) }, "") })
	}
	label := i18n.T("选择…")
	if p.editing {
		label = i18n.T("完成")
	}
	ui.Button(c, label).OnClick(func() {
		if !p.editing {
			p.loadEditor(st.Settings)
		}
		p.editing = !p.editing
	})
	ui.Button(c, "").Label(i18n.T("更多")).Tooltip(i18n.T("更多")).Children(func() { e.Kit.Icon(c, "more", 14) }).Menu(func(m *ui.Menu) {
		if m.Item(i18n.T("只看已解密")).Checked(p.onlyDec).Chosen() {
			p.onlyDec = !p.onlyDec
		}
		m.Separator()
		if m.Item(i18n.T("导出 HAR（不含正文）")).Disabled(!st.Enabled).Chosen() {
			p.exportHAR(false)
		}
		if m.Item(i18n.T("导出 HAR（含正文）…")).Disabled(!st.Enabled).Chosen() {
			e.Confirm(i18n.T("导出含正文的 HAR？"), i18n.T("正文中的凭据只对 JSON 与表单的常见字段打码，网页内容、GraphQL 变量、路径中的令牌不会打码；文件为明文。导出前请确认接收方可信。"),
				i18n.T("导出"), true, func() { p.exportHAR(true) })
		}
		m.Separator()
		if m.Item(i18n.T("清空记录")).Disabled(st.Flows == 0).Chosen() {
			p.sel = 0
			e.Run(e.API.ClearCaptureFlows, "")
		}
		m.Separator()
		if m.Item(i18n.T("导出根证书…")).Disabled(st.CA == model.CANone).Chosen() {
			p.exportCA()
		}
		if m.Item(i18n.T("重新生成根证书…")).Disabled(st.CA == model.CANone).Chosen() {
			e.Confirm(i18n.T("重新生成根证书？"), i18n.T("会生成新的根证书并请求系统信任，旧根证书的信任和私钥随后移除；正在解密的连接会断开。"), i18n.T("重新生成"), false,
				func() { e.Run(e.API.RegenerateCA, i18n.T("已重新生成根证书")) })
		}
		if m.Item(i18n.T("移除根证书并删除私钥…")).Disabled(st.CA == model.CANone).Chosen() {
			e.Confirm(i18n.T("移除根证书？"), i18n.T("会关闭解密，从系统信任中移除澜流根证书并删除私钥。下次开启解密需要重新安装。"), i18n.T("移除"), true,
				func() { e.Run(e.API.RemoveCACert, i18n.T("已移除根证书")) })
		}
	})
}

func (p *Page) banners(c *ui.Context, st model.CaptureStatus) {
	e := p.e
	switch st.CA {
	case model.CANone, model.CAUntrusted:
		msg := i18n.T("还没有安装澜流根证书：解密需要系统信任它（只装在当前用户下，有效期 90 天，不能为银行、登录等域名和任何 IP 签发证书）。")
		if st.CA == model.CAUntrusted {
			msg = i18n.T("系统尚未信任澜流根证书（可能取消了授权，或被设备管理策略禁止）。")
		}
		e.Kit.Banner(c, "warn", msg, func() {
			ui.Button(c, i18n.T("安装根证书")).OnClick(func() { e.Run(e.API.InstallCACert, i18n.T("根证书已安装并受信任")) })
		})
	case model.CAExpiring:
		e.Kit.Banner(c, "warn", i18n.T("根证书 7 天内过期，请重新生成。"), func() {
			ui.Button(c, i18n.T("重新生成")).OnClick(func() { e.Run(e.API.RegenerateCA, i18n.T("已重新生成根证书")) })
		})
	}
	if st.Enabled && st.Paused {
		e.Kit.Banner(c, "info", i18n.Tf("解密已暂停：%s。切回规则模式并运行内核后自动恢复。", st.PauseReason), nil)
	}
	if !st.FindProcess {
		e.Kit.Banner(c, "info", i18n.T("按程序解密需要识别进程：高级设置中 find-process-mode 为 off，抓包期间已临时改为 strict。"), nil)
	}
	if st.Error != "" {
		e.Kit.Banner(c, "error", st.Error, nil)
	}
}

// intro is the first-run explanation (docs/capture.md §8 法律).
func (p *Page) intro(c *ui.Context, t *ui.Theme, st model.CaptureStatus) {
	e := p.e
	e.Kit.Section(c, i18n.T("开始之前"), "", func() {
		ui.Column(c).Padding(12).Gap(8).Children(func() {
			for _, l := range []string{
				i18n.T("HTTPS 解密只用于查看本机上你自己的应用发出了什么。不会解密局域网中其他设备的流量。"),
				i18n.T("澜流会安装一个只属于本机的根证书；它能为你选中的网站签发证书。私钥不离开本机，关闭解密 7 天后自动移除。"),
				i18n.T("解密期间会看到网页中他人的内容（例如消息、评论）。导出或分享记录前，请检查打码是否覆盖了凭据。"),
				i18n.T("公司设备上安装根证书可能违反管理规定，或被安全软件拦截。"),
				i18n.T("证书固定的应用、Firefox / Java / Node 等自带信任库的程序不会信任澜流根证书，它们的请求会失败或原样透传。系统代理模式下 HTTP/3（QUIC）不经过代理，看不到。"),
			} {
				ui.Text(c, "• "+l).FontSize(12).TextColor(t.Text)
			}
			ui.Checkbox(c.Key("ack"), &p.ack, i18n.T("我已了解，仅用于审计本机本人的应用"))
			ui.PrimaryButton(c, i18n.T("继续")).Disabled(!p.ack).OnClick(func() {
				set := st.Settings
				set.Acknowledged = true
				e.Run(func() error { return e.API.SaveCaptureSettings(set) }, "")
				p.loadEditor(set)
				p.editing = true
			})
		})
	})
}

func (p *Page) loadEditor(set model.CaptureSettings) {
	p.domains = strings.Join(set.Domains, "\n")
	p.bypass = strings.Join(set.Bypass, "\n")
	p.apps = append([]model.CaptureApp(nil), set.Apps...)
	p.off = append([]string(nil), set.DisabledBuiltin...)
	p.ports = set.Ports
	switch {
	case set.AutoOffMin < 0:
		p.autoOff = strconv.Itoa(model.CaptureDefaultAutoOff)
	default:
		p.autoOff = strconv.Itoa(set.AutoOffMin)
	}
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == ',' || r == ' ' }) {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// editor is the selection: domains, programs, skip list, timer.
func (p *Page) editor(c *ui.Context, t *ui.Theme, st model.CaptureStatus) {
	e := p.e
	if !st.Settings.Acknowledged {
		return
	}
	if !p.editing {
		p.loadEditor(st.Settings)
		p.editing = true
	}
	e.Kit.Section(c, i18n.T("要解密的域名"), i18n.T("每行一个：example.com 只匹配它自己，*.example.com 只匹配子域名，+.example.com 匹配它和全部子域名"), func() {
		ui.Column(c).Padding(10).Children(func() {
			ui.TextArea(c.Key("domains"), &p.domains).Font("monospace").FontSize(12).Lines(3, 10).Label(i18n.T("要解密的域名"))
		})
	})
	e.Kit.Section(c, i18n.T("要解密的程序"), i18n.T("在「连接」页右键一行选择「解密此程序…」添加；macOS 上会匹配整个 .app，包括其辅助进程"), func() {
		ui.Column(c).Padding(10).Gap(6).Children(func() {
			if len(p.apps) == 0 {
				ui.Text(c, i18n.T("暂无")).FontSize(12).TextColor(t.TextMuted)
			}
			for i, a := range p.apps {
				i := i
				ui.Row(c.Key(fmt.Sprintf("app%d", i))).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, a.Name).Bold().FontSize(12)
					ui.Text(c, a.Match).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1)
					ui.Spacer(c)
					ui.Button(c, i18n.T("移除")).OnClick(func() { p.apps = append(p.apps[:i:i], p.apps[i+1:]...) })
				})
			}
			ui.Text(c, i18n.T("Electron 应用（Slack、VS Code、Discord…）主进程的请求不信任系统根证书，会失败或透传；Node 程序可设置 NODE_EXTRA_CA_CERTS 指向导出的根证书。")).FontSize(11).TextColor(t.TextMuted)
		})
	})
	e.Kit.Section(c, i18n.T("跳过"), i18n.T("这些主机从不解密；内置条目可逐条停用"), func() {
		ui.Column(c).Padding(10).Gap(8).Children(func() {
			ui.TextArea(c.Key("bypass"), &p.bypass).Font("monospace").FontSize(12).Lines(2, 8).Label(i18n.T("自定义跳过"))
			ui.Button(c, i18n.Tf("内置跳过列表（%d 条，停用 %d 条）", len(model.CaptureBuiltinBypass), len(p.off))).Menu(func(m *ui.Menu) {
				for _, b := range model.CaptureBuiltinBypass {
					b := b
					on := true
					for _, o := range p.off {
						if o == b {
							on = false
						}
					}
					if m.Item(b).Checked(on).Chosen() {
						if on {
							p.off = append(p.off, b)
						} else {
							var keep []string
							for _, o := range p.off {
								if o != b {
									keep = append(keep, o)
								}
							}
							p.off = keep
						}
					}
				}
			})
		})
	})
	e.Kit.Section(c, i18n.T("选项"), "", func() {
		e.Kit.Row(c, i18n.T("自动关闭（分钟）"), i18n.T("0 表示不自动关闭"), func() {
			ui.TextInput(c.Key("autooff"), &p.autoOff).Width(80).Label(i18n.T("自动关闭（分钟）"))
		})
		e.Kit.Row(c, i18n.T("端口"), i18n.T("用 / 分隔，默认 80/443/8443"), func() {
			ui.TextInput(c.Key("ports"), &p.ports).Width(160).Placeholder(model.CaptureDefaultPorts).Label(i18n.T("端口"))
		})
	})
	ui.Row(c).Gap(8).Children(func() {
		ui.Spacer(c)
		ui.PrimaryButton(c, i18n.T("保存")).OnClick(func() { p.save(st.Settings) })
	})
}

func (p *Page) save(prev model.CaptureSettings) {
	e := p.e
	set := prev
	set.Domains = lines(p.domains)
	set.Bypass = lines(p.bypass)
	set.Apps = append([]model.CaptureApp(nil), p.apps...)
	set.DisabledBuiltin = append([]string(nil), p.off...)
	set.Ports = strings.TrimSpace(p.ports)
	n, err := strconv.Atoi(strings.TrimSpace(p.autoOff))
	if err != nil || n < 0 {
		e.Toast(i18n.T("自动关闭时间应为不小于 0 的分钟数"), true)
		return
	}
	save := func() {
		set.AutoOffMin = n
		e.Run(func() error { return e.API.SaveCaptureSettings(set) }, i18n.T("已保存"))
		p.editing = false
	}
	if n == 0 && prev.AutoOffMin != 0 {
		e.Confirm(i18n.T("不自动关闭解密？"), i18n.T("解密会一直开着，直到你手动关闭。根证书在开启期间始终受信任。"), i18n.T("不自动关闭"), true, save)
		return
	}
	save()
}

// handleArg: "add-host:<host>" or "add-app:<path>\n<name>", from the 连接
// page or a row here. Before the first-run explanation is accepted the
// selection editor opens with it after adding.
func (p *Page) handleArg(a string, st model.CaptureStatus) {
	e := p.e
	enable := st.CA == model.CATrusted && st.Settings.Acknowledged
	okLabel := i18n.T("添加")
	tail := ""
	if enable && !st.Enabled {
		okLabel = i18n.T("添加并开启解密")
	}
	if !enable {
		tail = "\n\n" + i18n.T("添加后请先在本页安装根证书并阅读说明，再开启解密。")
	}
	switch {
	case strings.HasPrefix(a, "add-host:"):
		host := strings.TrimPrefix(a, "add-host:")
		e.Confirm(i18n.Tf("解密 %s？", host), i18n.Tf("将添加选择项「%s」（只匹配这个主机名；要包含子域名请在选择中写成 +.%s）。", host, host)+tail, okLabel, false, func() {
			e.Run(func() error { return e.API.AddCaptureDomain(host, enable) }, "")
			p.afterAdd(st)
		})
	case strings.HasPrefix(a, "add-app:"):
		path, name, _ := strings.Cut(strings.TrimPrefix(a, "add-app:"), "\n")
		sel := app.CaptureAppFor(path, name)
		e.Confirm(i18n.Tf("解密程序 %s？", sel.Name), i18n.Tf("将添加选择项「%s」：这个程序（及其辅助进程）访问的所有 HTTPS 主机都会解密，跳过列表除外。", sel.Match)+tail, okLabel, false, func() {
			e.Run(func() error { return e.API.AddCaptureApp(path, name, enable) }, "")
			p.afterAdd(st)
		})
	}
}

func (p *Page) exportHAR(bodies bool) {
	e := p.e
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: i18n.T("导出 HAR"), DefaultPath: "lanflow-capture.har"})
	if err != nil || path == "" {
		return
	}
	if filepath.Ext(path) == "" {
		path += ".har"
	}
	e.Run(func() error { return e.API.ExportHAR(path, bodies) }, i18n.Tf("已导出到 %s", filepath.Base(path)))
}

func (p *Page) exportCA() {
	e := p.e
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: i18n.T("导出根证书"), DefaultPath: "lanflow-ca.pem"})
	if err != nil || path == "" {
		return
	}
	e.Run(func() error { return e.API.ExportCACert(path) }, i18n.Tf("已导出到 %s", filepath.Base(path)))
}

// ---- list ----

func (p *Page) rows() []model.CaptureFlow {
	all := p.e.API.CaptureFlows()
	q := strings.ToLower(strings.TrimSpace(p.query))
	out := make([]model.CaptureFlow, 0, len(all))
	if !p.onlyDec {
		all = append(all, p.connRows()...)
	}
	for _, f := range all {
		if p.onlyDec && !f.Has(model.FlowDecrypted) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(f.Host+" "+f.Path+" "+f.Method+" "+f.Process+" "+strconv.Itoa(f.Status)+" "+f.Error), q) {
			continue
		}
		out = append(out, f)
	}
	less := func(a, b model.CaptureFlow) bool {
		switch p.sort.Column {
		case "process":
			return a.Process < b.Process
		case "method":
			return a.Method < b.Method
		case "status":
			return a.Status < b.Status
		case "host":
			return a.Host < b.Host
		case "path":
			return a.Path < b.Path
		case "size":
			return a.RespSize < b.RespSize
		case "dur":
			return a.DurationMs < b.DurationMs
		}
		return a.ID < b.ID
	}
	sort.SliceStable(out, func(i, j int) bool {
		if p.sort.Descending {
			return less(out[j], out[i])
		}
		return less(out[i], out[j])
	})
	return out
}

func (p *Page) body(c *ui.Context, t *ui.Theme, st model.CaptureStatus) {
	e := p.e
	rows := p.rows()
	if len(rows) == 0 {
		switch {
		case strings.TrimSpace(p.query) != "":
			e.Kit.Empty(c, "search", i18n.Tf("没有匹配“%s”的请求", strings.TrimSpace(p.query)), "", nil)
		case p.onlyDec:
			e.Kit.Empty(c, "conns", i18n.T("还没有解密的请求"), i18n.T("在列表中右键一个请求选择「解密此主机」或「解密此程序」，之后的请求会显示完整内容。"), nil)
		default:
			e.Kit.Empty(c, "conns", i18n.T("等待请求"), i18n.T("本机发出的请求会出现在这里。选中一个可以解密它的主机或程序，查看 HTTPS 内容。"), nil)
		}
		return
	}
	p.selected = -1
	for i, r := range rows {
		if r.ID == p.sel {
			p.selected = i
		}
	}
	p.list.Key = func(i int) any { return rows[i].ID }
	p.list.Label = func(i int) string { return rows[i].Host + redact.Path(rows[i].Path) }
	p.list.CellMenu = func(m *ui.Menu, row, col int) { p.rowMenu(c, m, rows[row], st) }
	cols := env.I18nColumns(columns)
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Table(c.Key("table"), &p.list, cols, len(rows), func(row, col int) {
			p.cell(c, t, rows[row], cols[col].ID)
		}).Grow(1).FillHeight().Label(i18n.T("请求"))
		if p.selected >= 0 && p.selected < len(rows) {
			p.sel = rows[p.selected].ID
		} else {
			p.sel = 0
		}
		switch {
		case p.sel < 0:
			p.connDetail(c, t, rows[p.selected], st)
		case p.sel > 0:
			p.detail(c, t, rows[p.selected])
		}
	})
}

func (p *Page) rowMenu(c *ui.Context, m *ui.Menu, f model.CaptureFlow, st model.CaptureStatus) {
	if f.ID < 0 {
		p.decryptItems(m, f, st)
		m.Separator()
	}
	if m.Item(i18n.T("复制 URL")).Disabled(f.Method == "").Chosen() {
		c.WriteClipboard(flowURL(f, true))
		p.e.Toast(i18n.T("已复制（查询参数中的凭据已打码）"), false)
	}
	if m.Item(i18n.T("复制主机")).Chosen() {
		c.WriteClipboard(f.Host)
		p.e.Toast(i18n.T("已复制"), false)
	}
}

func flowURL(f model.CaptureFlow, masked bool) string {
	scheme := f.Scheme
	if scheme == "" {
		scheme = "https"
	}
	host := f.Host
	if f.Port > 0 && !(scheme == "https" && f.Port == 443) && !(scheme == "http" && f.Port == 80) {
		host += ":" + strconv.Itoa(f.Port)
	}
	path := f.Path
	if masked {
		path = redact.Path(path)
	}
	return scheme + "://" + host + path
}

func (p *Page) cell(c *ui.Context, t *ui.Theme, f model.CaptureFlow, col string) {
	muted := t.TextMuted
	switch col {
	case "time":
		ui.Text(c, f.Start.Format("15:04:05")).FontFeatures("tnum").TextColor(muted).SingleLine()
	case "process":
		ui.Row(c).Gap(5).AlignItems(ui.Center).FillWidth().Children(func() {
			if f.Process != "" {
				p.icons.Draw(c, t, f.ProcessPath, procicon.Size)
			}
			name := f.Process
			if name == "" {
				name = "—"
			}
			ui.Text(c, name).TextColor(muted).SingleLine().Shrink(1)
		})
	case "method":
		ui.Text(c, env.Dash(f.Method)).SingleLine()
	case "status":
		s := "—"
		col := muted
		if f.Status > 0 {
			s = strconv.Itoa(f.Status)
			col = t.Text
			if f.Status >= 400 {
				col = t.Danger
			}
		}
		ui.Text(c, s).FontFeatures("tnum").TextColor(col)
	case "host":
		ui.Text(c, f.Host).SingleLine()
	case "path":
		ui.Text(c, env.Dash(redact.Path(f.Path))).TextColor(muted).SingleLine()
	case "size":
		ui.Text(c, env.FmtBytes(f.RespSize)).FontFeatures("tnum").SingleLine()
	case "dur":
		if !f.Done {
			ui.Spinner(c).Size(10, 10).Label(i18n.T("进行中"))
			return
		}
		ui.Text(c, fmt.Sprintf("%d ms", f.DurationMs)).FontFeatures("tnum").TextColor(muted).SingleLine()
	case "flags":
		ui.Row(c).Gap(4).Children(func() {
			for _, fl := range f.Flags {
				if txt, kind := flagText(fl); txt != "" {
					p.e.Kit.Tag(c, txt, kind)
				}
			}
		})
	}
}

func flagText(fl string) (string, string) {
	switch fl {
	case model.FlowPassthrough:
		return i18n.T("透传"), "gray"
	case model.FlowPinned:
		return i18n.T("证书固定"), "orange"
	case model.FlowECH:
		return "ECH", "gray"
	case model.FlowUpstreamBad:
		return i18n.T("上游证书无效"), "orange"
	case model.FlowClientCert:
		return i18n.T("客户端证书"), "orange"
	case model.FlowWebSocket:
		return "WS", "accent"
	case model.FlowTruncated:
		return i18n.T("已截断"), "gray"
	case model.FlowLoop:
		return i18n.T("回环"), "red"
	case model.FlowNotLocal:
		return i18n.T("非本机"), "red"
	case model.FlowUnknown:
		return i18n.T("未知协议"), "gray"
	case flowConn:
		return i18n.T("未解密"), "gray"
	}
	return "", ""
}

// ---- detail ----

func (p *Page) fetch(id int64) {
	p.det.Lock()
	if p.det.id == id && (p.det.loading || p.det.err != "" || p.det.d.Flow.ID == id) {
		p.det.Unlock()
		return
	}
	p.det.id, p.det.loading, p.det.err = id, true, ""
	p.det.d = model.CaptureFlowDetail{}
	p.det.Unlock()
	api, redraw := p.e.API, p.e.Redraw
	go func() {
		d, err := api.CaptureFlow(id)
		p.det.Lock()
		if p.det.id == id {
			p.det.loading = false
			p.det.d = d
			if err != nil {
				p.det.err = err.Error()
			}
		}
		p.det.Unlock()
		redraw()
	}()
}

func (p *Page) detail(c *ui.Context, t *ui.Theme, f model.CaptureFlow) {
	p.fetch(f.ID)
	p.det.Lock()
	d, loading, errText := p.det.d, p.det.loading, p.det.err
	p.det.Unlock()
	if d.Flow.ID == f.ID && d.Flow.Done != f.Done {
		// the request finished since: fetch the rest
		p.det.Lock()
		p.det.d.Flow.ID = 0
		p.det.id = 0
		p.det.Unlock()
	}
	ui.Column(c.Key("detail")).Width(detailWidth).FillHeight().BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() {
		ui.Row(c).Padding(12, 14, 6, 14).Gap(6).AlignItems(ui.Start).Children(func() {
			ui.Column(c).Grow(1).Gap(2).Children(func() {
				title := f.Host
				if f.Method != "" {
					title = f.Method + " " + flowURL(f, !p.reveal)
				}
				ui.Text(c, title).Bold().FontSize(12).Selectable()
				meta := strings.ToUpper(f.Proto)
				if f.Status > 0 {
					meta += " · " + strconv.Itoa(f.Status)
				}
				if f.Error != "" {
					meta += " · " + f.Error
				}
				ui.Text(c, meta).FontSize(11).TextColor(t.TextMuted)
			})
			p.closeButton(c)
		})
		tabs := []string{i18n.T("概要"), i18n.T("请求头"), i18n.T("请求体"), i18n.T("响应头"), i18n.T("响应体")}
		if len(d.WSFrames) > 0 {
			tabs = append(tabs, "WebSocket")
		}
		if p.tab >= len(tabs) {
			p.tab = 0
		}
		ui.Row(c).Padding(0, 14).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Segmented(c.Key("dtab"), &p.tab, tabs...).Label(i18n.T("详情页签")).Shrink(1)
		})
		ui.Row(c).Padding(4, 14).Children(func() {
			ui.Checkbox(c.Key("reveal"), &p.reveal, i18n.T("显示打码的原值"))
		})
		ui.Scroll(c).Grow(1).Padding(6, 14, 14, 14).Gap(8).Selectable().Children(func() {
			switch {
			case errText != "":
				ui.Text(c, errText).FontSize(12).TextColor(t.Danger)
				return
			case loading || d.Flow.ID != f.ID:
				ui.Spinner(c).Size(14, 14).Label(i18n.T("加载中"))
				return
			}
			switch p.tab {
			case 0:
				p.summary(c, t, d)
			case 1:
				p.headers(c, t, d.ReqHeader)
			case 2:
				p.bodyView(c, t, d.Flow.ID, d.ReqBody, d.ReqBodySize, d.ReqHeader)
			case 3:
				p.headers(c, t, d.RespHeader)
			case 4:
				p.bodyView(c, t, d.Flow.ID, d.RespBody, d.RespBodySize, d.RespHeader)
			case 5:
				p.frames(c, t, d.WSFrames)
			}
		})
	})
}

func kv(c *ui.Context, t *ui.Theme, k, v string) {
	ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
		ui.Text(c, k).Width(84).FontSize(12).TextColor(t.TextMuted).Unselectable()
		ui.Text(c, v).FontSize(12).FontFeatures("tnum").Grow(1)
	})
}

func (p *Page) summary(c *ui.Context, t *ui.Theme, d model.CaptureFlowDetail) {
	f := d.Flow
	if f.Method != "" {
		kv(c, t, "URL", flowURL(f, !p.reveal))
	}
	kv(c, t, i18n.T("开始"), f.Start.Format("2006-01-02 15:04:05.000"))
	proc := env.Dash(f.Process)
	switch f.Attribution {
	case "ambiguous":
		proc += i18n.T("（归属不确定）")
	case "unknown":
		proc += i18n.T("（归属未知）")
	}
	kv(c, t, i18n.T("程序"), proc)
	if f.ProcessPath != "" {
		kv(c, t, i18n.T("路径"), f.ProcessPath)
	}
	kv(c, t, i18n.T("选择项"), env.Dash(f.Selector))
	if f.Error != "" {
		kv(c, t, i18n.T("说明"), f.Error)
	}
	if d.TLS.Version != "" {
		kv(c, t, "TLS", d.TLS.Version+" · "+d.TLS.Cipher)
		kv(c, t, "ALPN", i18n.Tf("%s（客户端提供 %s）", env.Dash(d.TLS.ALPN), strings.Join(d.TLS.ClientALPN, ", ")))
		kv(c, t, "SNI", d.TLS.SNI)
		if d.TLS.ECH {
			kv(c, t, "ECH", i18n.T("ClientHello 带 ECH 扩展（GREASE）"))
		}
		for i, fp := range d.TLS.UpstreamCert {
			label := ""
			if i == 0 {
				label = i18n.T("上游证书")
			}
			kv(c, t, label, fp[:min(len(fp), 32)]+"…")
		}
	}
	tm := d.Timing
	if tm.EndMs > 0 || tm.TTFBMs > 0 {
		kv(c, t, i18n.T("时间线"), i18n.Tf("上游连接 %d ms · 上游 TLS %d ms · 客户端 TLS %d ms · 首字节 %d ms · 完成 %d ms",
			tm.UpstreamDialMs, tm.UpstreamTLSMs, tm.ClientTLSMs, tm.TTFBMs, tm.EndMs))
	}
	kv(c, t, i18n.T("大小"), i18n.Tf("请求 %s · 响应 %s", env.FmtBytes(f.ReqSize), env.FmtBytes(f.RespSize)))
}

func (p *Page) headers(c *ui.Context, t *ui.Theme, h []model.KV) {
	if len(h) == 0 {
		ui.Text(c, i18n.T("无")).FontSize(12).TextColor(t.TextMuted)
		return
	}
	if !p.reveal {
		h = redact.Headers(h)
	}
	for _, x := range h {
		ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
			ui.Text(c, x.Key).Width(130).FontSize(12).Font("monospace").TextColor(t.TextMuted)
			ui.Text(c, x.Value).FontSize(12).Font("monospace").Grow(1)
		})
	}
}

func header(h []model.KV, name string) string {
	for _, x := range h {
		if strings.EqualFold(x.Key, name) {
			return x.Value
		}
	}
	return ""
}

const previewLimit = 256 << 10

func (p *Page) bodyView(c *ui.Context, t *ui.Theme, id int64, body []byte, size int64, h []model.KV) {
	if size == 0 && len(body) == 0 {
		ui.Text(c, i18n.T("无正文")).FontSize(12).TextColor(t.TextMuted)
		return
	}
	if size > int64(len(body)) {
		ui.Text(c, i18n.Tf("已截断：记录了 %s，原大小 %s", env.FmtBytes(int64(len(body))), env.FmtBytes(size))).FontSize(11).TextColor(t.TextMuted)
	}
	enc := header(h, "content-encoding")
	dec, ok := redact.Decode(body, enc)
	if !ok {
		ui.Text(c, i18n.Tf("正文以 %s 压缩，无法预览（%s）", enc, env.FmtBytes(int64(len(body))))).FontSize(12).TextColor(t.TextMuted)
		return
	}
	ct := header(h, "content-type")
	kind := redact.Kind(ct, dec)
	switch kind {
	case "image":
		p.image(c, t, id, dec)
		return
	case "binary":
		ui.Text(c, i18n.Tf("二进制内容（%s）", env.FmtBytes(int64(len(dec))))).FontSize(12).TextColor(t.TextMuted)
		ui.Text(c, hexdump(dec, 512)).FontSize(11).Font("monospace")
		return
	}
	show := dec
	if !p.reveal {
		show = redact.Body(dec, ct)
	} else if kind == "json" {
		var b bytes.Buffer
		if json.Indent(&b, dec, "", "  ") == nil {
			show = b.Bytes()
		}
	}
	if kind == "form" {
		show = []byte(strings.ReplaceAll(string(show), "&", "\n"))
	}
	if len(show) > previewLimit {
		show = show[:previewLimit]
		ui.Text(c, i18n.T("只显示前 256 KiB")).FontSize(11).TextColor(t.TextMuted)
	}
	if !p.reveal && (kind == "text" || kind == "multipart") {
		ui.Text(c, i18n.T("文本正文不做打码，可能包含凭据。")).FontSize(11).TextColor(t.TextMuted)
	}
	ui.Text(c, string(bytes.ToValidUTF8(show, []byte("�")))).FontSize(12).Font("monospace")
}

func (p *Page) image(c *ui.Context, t *ui.Theme, id int64, b []byte) {
	p.det.Lock()
	src := p.det.img
	if p.det.imgFor != id {
		src = nil
		if img, _, err := image.Decode(bytes.NewReader(b)); err == nil {
			src = ui.NewBitmap(img)
		}
		p.det.img, p.det.imgFor = src, id
	}
	p.det.Unlock()
	if src == nil {
		ui.Text(c, i18n.Tf("图片（%s），无法预览此格式", env.FmtBytes(int64(len(b))))).FontSize(12).TextColor(t.TextMuted)
		return
	}
	ui.Image(c, src).Width(detailWidth - 40).Radius(4)
}

func (p *Page) frames(c *ui.Context, t *ui.Theme, fs []model.CaptureWSFrame) {
	for _, f := range fs {
		dir := "↑"
		if f.FromPeer {
			dir = "↓"
		}
		text := f.Text
		if text == "" {
			text = i18n.Tf("操作码 %d · %s", f.Opcode, env.FmtBytes(f.Size))
		}
		ui.Row(c).Gap(6).AlignItems(ui.Start).Children(func() {
			ui.Text(c, f.Time.Format("15:04:05")+" "+dir).FontSize(11).TextColor(t.TextMuted).Font("monospace")
			ui.Text(c, text).FontSize(12).Font("monospace").Grow(1)
		})
	}
}

func hexdump(b []byte, max int) string {
	if len(b) > max {
		b = b[:max]
	}
	var sb strings.Builder
	for i := 0; i < len(b); i += 16 {
		end := min(i+16, len(b))
		fmt.Fprintf(&sb, "%04x  % x\n", i, b[i:end])
	}
	return sb.String()
}
