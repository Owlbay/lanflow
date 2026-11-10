// Package env is the contract between the window shell (package view) and
// the page packages (view/overview, view/proxies, …).
//
// The exported API of this package is a stable contract between pages.
//
// Every page package exports:
//
//	type Page struct{ ... }            // page-local UI state (filters, selections)
//	func New(e *env.Env) *Page
//	func (p *Page) View(c *ui.Context) // draws the whole content area incl. header
//
// Pages read state from e.API on every build (reads are cheap snapshots)
// and run actions with e.Run (never call API actions on the UI thread).
package env

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// Page identifies a sidebar destination.
type Page string

const (
	Overview Page = "overview"
	Proxies  Page = "proxies"
	Subs     Page = "subs"
	Conns    Page = "conns"
	Logs     Page = "logs"
	Rules    Page = "rules"
	Net      Page = "net"
	Stats    Page = "stats"
	Probe    Page = "probe"
	Settings Page = "settings"
)

// Order is the sidebar order (⌘1…⌘7 map to the first seven).
var Order = []Page{Overview, Proxies, Subs, Conns, Logs, Rules, Net, Stats, Probe, Settings}

// ImportURLPrefix starts the Subs navigation arg that opens the
// import-from-link dialog with a link prefilled (a lanflow:// or clash://
// deep link, docs/design.md §6.1 / §9): ImportURLPrefix + url, optionally
// followed by "\n" + name. The plain arg "import-url" opens it empty.
const ImportURLPrefix = "import-url:"

// ImportURLArg builds the Subs arg for ParseImportURL.
func ImportURLArg(url, name string) string {
	if name != "" {
		return ImportURLPrefix + url + "\n" + name
	}
	return ImportURLPrefix + url
}

// ParseImportURL reads an import arg: ok is true for both the plain
// "import-url" (empty url) and the prefixed form.
func ParseImportURL(arg string) (url, name string, ok bool) {
	if arg == "import-url" {
		return "", "", true
	}
	if !strings.HasPrefix(arg, ImportURLPrefix) {
		return "", "", false
	}
	url, name, _ = strings.Cut(arg[len(ImportURLPrefix):], "\n")
	return url, name, true
}

// Title returns the page's Chinese title.
func Title(p Page) string {
	return map[Page]string{Overview: i18n.T("概览"), Proxies: i18n.T("代理"), Subs: i18n.T("订阅"), Conns: i18n.T("连接"), Logs: i18n.T("日志"), Rules: i18n.T("规则|页"), Net: i18n.T("网络"), Stats: i18n.T("统计"), Probe: i18n.T("检测"), Settings: i18n.T("设置")}[p]
}

// I18nColumns returns cols with their titles translated (i18n.T), for a
// table whose column list is a package-level table of Chinese titles.
func I18nColumns(cols []ui.TableColumn) []ui.TableColumn {
	out := make([]ui.TableColumn, len(cols))
	for i, col := range cols {
		col.Title = i18n.T(col.Title)
		out[i] = col
	}
	return out
}

// Env is shared by all pages of one window.
type Env struct {
	API app.API

	// Navigate switches the sidebar page; arg is an optional page-specific
	// target (e.g. a group name for Proxies, a settings tab for Settings).
	Navigate func(p Page, arg string)
	// Arg returns and clears the pending navigation arg for p.
	Arg func(p Page) string

	// Toast shows a transient message at the bottom of the window. Error
	// toasts stay 5 s and offer 「查看详情」 with the full text (docs/design.md §6.5).
	Toast func(msg string, isError bool)

	// ToastDetail is Toast with a separate detail: the toast shows msg and
	// 「查看详情」 opens a dialog with detail (the full error, a command's
	// output…). Nil in Env values built before the shell existed.
	ToastDetail func(msg, detail string, isError bool)

	// Run runs job off the UI thread; on error it toasts the error text,
	// on success it toasts okMsg (if non-empty); it always redraws.
	Run func(job func() error, okMsg string)

	// Confirm shows a modal confirmation; onOK runs on the UI thread.
	Confirm func(title, body, okLabel string, danger bool, onOK func())

	// Redraw requests a new frame (safe from any goroutine).
	Redraw func()

	// Mac is true on macOS (labels: 访达/菜单栏 vs 资源管理器/通知区域, ⌘ vs Ctrl).
	Mac bool

	// SetGlobalShortcut makes acc (a MyGo accelerator, "" = none) the
	// system-wide shortcut that toggles the system proxy, replacing the
	// one registered. It returns a user-facing error, and keeps the
	// previous shortcut, when the system refuses the keys (held by
	// another app). Nil in Env values without a shell (tests): the
	// settings page then accepts any shortcut.
	SetGlobalShortcut func(acc string) error

	// Kit draws shared components so all pages look alike.
	Kit Kit

	// UIState saves the remembered presentation state through one
	// debounced writer (SaveUIState); nil in Env values without a shell
	// (tests), which then save at once.
	UIState *UIStateSaver
}

// Kit draws the shared, native-looking building blocks (see docs/design.md §3).
// The shell provides the real implementation; Basic() is a plain fallback
// used by tests.
type Kit interface {
	// Header draws the page title row (20 pt semibold title, optional muted
	// subtitle after it, tools right-aligned).
	Header(c *ui.Context, title, subtitle string, tools func())
	// Section draws a group heading (with optional muted note) and a rounded
	// group box containing body (rows separated by hairlines).
	Section(c *ui.Context, title, note string, body func())
	// Row draws one settings-style row: label (+ muted desc below) on the
	// left, control on the right.
	Row(c *ui.Context, label, desc string, control func())
	// Empty draws a centered empty state with icon name, title, description and actions.
	Empty(c *ui.Context, icon, title, desc string, actions func())
	// Banner draws an inline message bar; kind is "info", "warn" or "error".
	Banner(c *ui.Context, kind, msg string, actions func())
	// Tag draws a small pill; kind is "accent", "gray", "green", "orange", "red".
	Tag(c *ui.Context, text, kind string)
	// Delay draws a latency label: green ≤300, orange ≤800, red >800 or
	// "超时" for -1, "—" for 0, a spinner when testing.
	Delay(c *ui.Context, ms int, testing bool)
	// Dot draws a status dot; kind is "green", "orange", "red", "gray", "accent".
	Dot(c *ui.Context, kind string)
	// Icon draws a monochrome line icon by name (overview, proxies, subs,
	// conns, logs, rules, net, settings, search, more, plus, refresh, bolt,
	// pause, play, copy, close, down, warn, check, doc, power, link).
	Icon(c *ui.Context, name string, size float32)
}

// basic is a minimal Kit used until the shell provides the real one.
type basic struct{}

// Basic returns a minimal Kit.
func Basic() Kit { return basic{} }

func (basic) Header(c *ui.Context, title, subtitle string, tools func()) {
	ui.Row(c).Gap(8).Padding(12).Children(func() {
		ui.Text(c, title).FontSize(20).Bold()
		if subtitle != "" {
			ui.Text(c, subtitle).TextColor(c.Theme().TextMuted)
		}
		ui.Spacer(c)
		if tools != nil {
			tools()
		}
	})
}
func (basic) Section(c *ui.Context, title, note string, body func()) {
	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, title).Bold()
		if body != nil {
			body()
		}
	})
}
func (basic) Row(c *ui.Context, label, desc string, control func()) {
	ui.Row(c).Gap(10).Children(func() {
		ui.Text(c, label)
		ui.Spacer(c)
		if control != nil {
			control()
		}
	})
}
func (basic) Empty(c *ui.Context, icon, title, desc string, actions func()) {
	ui.Column(c).Fill().Center().Gap(8).Children(func() {
		ui.Text(c, title).Bold()
		ui.Text(c, desc).TextColor(c.Theme().TextMuted)
		if actions != nil {
			ui.Row(c).Gap(8).Children(actions)
		}
	})
}
func (basic) Banner(c *ui.Context, kind, msg string, actions func()) {
	ui.Row(c).Gap(8).Children(func() {
		ui.Text(c, msg)
		if actions != nil {
			actions()
		}
	})
}
func (basic) Tag(c *ui.Context, text, kind string)          { ui.Badge(c, text) }
func (basic) Delay(c *ui.Context, ms int, testing bool)     { ui.Text(c, FmtDelay(ms, testing)) }
func (basic) Dot(c *ui.Context, kind string)                { ui.Text(c, "●") }
func (basic) Icon(c *ui.Context, name string, size float32) {}

// CoreDownloadText describes a core download in progress, e.g.
// "正在下载 Mihomo v1.19.33… 12.3 MB / 20.1 MB（61%）"; "" when idle.
func CoreDownloadText(dl model.CoreDownload) string {
	if !dl.Active {
		return ""
	}
	switch dl.Phase {
	case "下载":
		s := i18n.T("正在下载 Mihomo ") + dl.Version + "…"
		switch {
		case dl.Total > 0:
			s += fmt.Sprintf(i18n.T(" %s / %s（%d%%）"), FmtBytes(dl.Bytes), FmtBytes(dl.Total), dl.Bytes*100/dl.Total)
		case dl.Bytes > 0:
			s += " " + FmtBytes(dl.Bytes)
		}
		return s
	case "校验":
		return i18n.T("正在校验 Mihomo ") + dl.Version + "…"
	case "安装":
		return i18n.T("正在安装 Mihomo ") + dl.Version + "…"
	case "启动":
		return i18n.T("正在启动新内核…")
	}
	return i18n.T("正在处理 Mihomo ") + dl.Version + "…"
}
