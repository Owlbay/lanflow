// Package logs is the 日志 page (docs/design.md §5.5): the core's log, or
// Lanflow's own run log, in a virtualized monospace list with level filter,
// search, pause, clear and follow-bottom; its ⋯ menu also exports the
// diagnostics bundle.
package logs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

var (
	levels     = []string{"debug", "info", "warning", "error"}
	levelNames = map[string]string{"debug": "调试", "info": "信息", "warning": "警告", "error": "错误"}
	levelRank  = map[string]int{"debug": 0, "info": 1, "warning": 2, "error": 3}
)

// Page holds page-local UI state.
type Page struct {
	e *env.Env

	query    string
	search   ui.Handle // the search field, focused by ⌘F
	level    int       // index into levels of the lowest level shown
	paused   bool
	frozen   []model.LogEntry
	list     ui.ListState
	endCount int // how many entries there were when the list last showed its end
	menuOpen bool
	source   int  // 0 = the core's log, 1 = Lanflow's own
	loaded   bool // level/source restored from API.UIState
}

// isApp reports whether Lanflow's own log is shown.
func (p *Page) isApp() bool { return p.source == 1 }

// entries returns the log of the chosen source.
func (p *Page) entries() []model.LogEntry {
	if p.isApp() {
		return p.e.API.AppLogs()
	}
	return p.e.API.Logs()
}

// New returns the page.
func New(e *env.Env) *Page {
	p := &Page{e: e, level: levelRank["info"]} // 级别：信息 by default (docs/design.md §5.5)
	p.list.FollowEnd = true
	return p
}

// minLevel returns the lowest level shown.
func (p *Page) minLevel() int { return p.level }

// restore loads the remembered level filter and source once
// (model.UIState.LogLevel / LogSource).
func (p *Page) restore() {
	if p.loaded {
		return
	}
	p.loaded = true
	u := p.e.API.UIState()
	if r, ok := levelRank[u.LogLevel]; ok {
		p.level = r
	}
	if u.LogSource == "app" {
		p.source = 1
	}
}

// persist remembers the level filter and source off the UI thread,
// keeping what other pages store in the UI state.
func (p *Page) persist() {
	level, source := levels[min(p.level, len(levels)-1)], "core"
	if p.isApp() {
		source = "app"
	}
	p.e.SaveUIState("logs", func(u *model.UIState) {
		u.LogLevel, u.LogSource = level, source
	})
}

// levelOptions are the level select's texts, 「级别：信息」, in the language
// of the frame.
func levelOptions() []string {
	opts := make([]string, len(levels))
	for i, l := range levels {
		opts[i] = i18n.Tf("级别：%s", i18n.T(levelNames[l]))
	}
	return opts
}

// View draws the whole content area including the header.
func (p *Page) View(c *ui.Context) {
	e, t := p.e, c.Theme()
	p.restore()
	all := p.entries()
	if p.paused {
		if p.frozen == nil {
			p.frozen = all
		}
		all = p.frozen
	} else {
		p.frozen = nil
	}
	vis := p.visible(all)

	ui.Column(c).Fill().Children(func() {
		e.Kit.Header(c, i18n.T("日志"), "", func() { p.tools(c, all) })
		if c.Shortcut(ui.Cmd, ui.KeyF) {
			p.search.Focus(c)
		}
		ui.Box(c).Grow(1).Children(func() {
			if len(vis) == 0 {
				switch {
				case strings.TrimSpace(p.query) != "":
					e.Kit.Empty(c, "search", i18n.Tf("没有匹配“%s”的日志", strings.TrimSpace(p.query)), "", nil)
				case p.paused:
					e.Kit.Empty(c, "logs", i18n.T("暂无日志"), i18n.T("已暂停。按「继续」恢复接收新日志。"), nil)
				case p.isApp():
					e.Kit.Empty(c, "logs", i18n.T("暂无日志"), i18n.T("澜流自身的操作和错误会记录在这里。"), nil)
				default:
					e.Kit.Empty(c, "logs", i18n.T("暂无日志"), i18n.T("内核运行时，连接、DNS 和错误记录会实时显示在这里；可用「级别」缩小范围。"), nil)
				}
				return
			}
			p.list.Label = func(i int) string { return vis[i].Payload }
			ui.List(c.Key("list"), &p.list, len(vis), func(i int) {
				p.line(c, t, vis[i])
			}).Fill().Font("monospace").FontSize(12).Selectable().KeepScrollbars(true).Label(i18n.T("日志"))
			atEnd := p.list.AtEnd() || len(vis) == 0
			if atEnd {
				p.endCount = len(all)
			}
			if !atEnd {
				label := i18n.T("回到底部")
				if len(all) > p.endCount {
					label += i18n.T("（有新日志）")
				}
				ui.Button(c, "").Children(func() {
					e.Kit.Icon(c, "down", 13)
					ui.Text(c, label)
				}).Radius(999).Shadow(0, 2, 8, 0, ui.RGBA(0, 0, 0, 0.18)).
					Attach(ui.AnchorBottom, ui.AnchorBottom).Bottom(14).
					OnClick(func() { p.list.ScrollToEnd() })
			}
		})
	})
}

func (p *Page) tools(c *ui.Context, all []model.LogEntry) {
	e := p.e
	if ui.Segmented(c.Key("source"), &p.source, i18n.T("内核"), i18n.T("澜流")).Label(i18n.T("日志来源")).
		Tooltip(i18n.T("内核：Mihomo 的运行日志；澜流：澜流自己的操作记录、提示和错误")).Changed() {
		p.frozen, p.paused = nil, false
		p.list.ScrollToEnd()
		p.persist()
	}
	ui.SearchField(c.Key("search"), &p.query).Placeholder(i18n.T("搜索")).Label(i18n.T("搜索日志")).Width(180).Bind(&p.search)
	opts := levelOptions()
	cur := opts[min(p.level, len(opts)-1)]
	if ui.Select(c.Key("level"), &cur, opts).Label(i18n.T("级别")).Changed() {
		for i, o := range opts {
			if o == cur {
				p.level = i
			}
		}
		p.persist()
	}
	pauseLabel, icon := i18n.T("暂停"), "pause"
	if p.paused {
		pauseLabel, icon = i18n.T("继续"), "play"
	}
	ui.Button(c, "").Label(pauseLabel).Children(func() {
		e.Kit.Icon(c, icon, 13)
		ui.Text(c, pauseLabel)
	}).OnClick(func() { p.paused = !p.paused })
	ui.Button(c, i18n.T("清空")).Disabled(len(all) == 0).OnClick(func() {
		p.frozen = nil
		app := p.isApp()
		e.Run(func() error {
			if app {
				e.API.ClearAppLogs()
			} else {
				e.API.ClearLogs()
			}
			return nil
		}, "")
	})
	reveal := i18n.T("在资源管理器中显示日志文件")
	if e.Mac {
		reveal = i18n.T("在访达中显示日志文件")
	}
	ui.Button(c, "⋯").Label(i18n.T("更多")).Tooltip(i18n.T("更多")).PopoverMenuButton(func(m *ui.Menu) {
		// 复制全部 copies every line held, whatever the level filter and
		// the search show (docs/design.md §5.5 ⋯ 菜单).
		if m.Item(i18n.T("复制全部")).Disabled(len(all) == 0).Chosen() {
			c.WriteClipboard(text(all))
			e.Toast(i18n.T("已复制"), false)
		}
		if m.Item(reveal).Chosen() {
			name := logFileName
			if p.isApp() {
				name = appLogFileName
			}
			revealLogFile(e.API.LogDir(), name)
		}
		m.Separator()
		if m.Item(i18n.T("导出诊断信息…")).Chosen() {
			go ExportDiagnostics(e)
		}
	}, ui.PopoverMenuButtonOptions{At: ui.AnchorBottomEnd, Self: ui.AnchorTopEnd, Open: &p.menuOpen})
}

// logFileName is the core's log file under LogDir (internal/app/service.go),
// appLogFileName Lanflow's own (internal/applog).
const (
	logFileName    = "mihomo.log"
	appLogFileName = "lanflow.log"
)

// ExportDiagnostics asks where to save the diagnostics zip and writes it.
// The system dialog blocks, so call it on its own goroutine.
func ExportDiagnostics(e *env.Env) {
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
		Title:       i18n.T("导出诊断信息"),
		Message:     i18n.T("包含日志、运行时配置和网络状态；节点地址、密码和订阅令牌已去除。"),
		DefaultPath: "lanflow-diagnostics-" + time.Now().Format("20060102-1504") + ".zip",
		Filters:     []mygo.FileFilter{{Name: i18n.T("ZIP 压缩包"), Extensions: []string{"zip"}}},
	})
	if err != nil || path == "" {
		return
	}
	if filepath.Ext(path) == "" {
		path += ".zip"
	}
	e.Run(func() error {
		if err := e.API.ExportDiagnostics(path); err != nil {
			return err
		}
		mygo.Shell.ShowItemInFolder(path)
		return nil
	}, i18n.Tf("已导出到 %s", filepath.Base(path)))
}

// revealLogFile selects the log file in the file manager; before it has
// ever been written, it opens the log folder instead.
func revealLogFile(dir, name string) {
	file := filepath.Join(dir, name)
	if _, err := os.Stat(file); err == nil {
		mygo.Shell.ShowItemInFolder(file)
		return
	}
	_ = mygo.Shell.OpenPath(dir)
}

func (p *Page) visible(all []model.LogEntry) []model.LogEntry {
	min := p.minLevel()
	q := strings.ToLower(strings.TrimSpace(p.query))
	out := make([]model.LogEntry, 0, len(all))
	for _, l := range all {
		if levelRank[l.Level] < min {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(l.Payload), q) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// text renders lines as the list shows them, one per line.
func text(lines []model.LogEntry) string {
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "%s %s %s\n", l.Time.Format("15:04:05"), levelName(l.Level), l.Payload)
	}
	return b.String()
}

func levelName(l string) string {
	if n, ok := levelNames[l]; ok {
		return i18n.T(n)
	}
	return l
}

// lineColors returns the text colour of a line and of its time column:
// a warning is orange and an error red as a whole line, time included;
// other lines keep the default colour with a muted time (docs/design.md
// §5.5 级别样式).
func lineColors(level string, t *ui.Theme) (text, time ui.Color) {
	switch level {
	case "warning":
		return t.Warning, t.Warning
	case "error":
		return t.Danger, t.Danger
	}
	return t.Text, t.TextMuted
}

func (p *Page) line(c *ui.Context, t *ui.Theme, l model.LogEntry) {
	color, timeColor := lineColors(l.Level, t)
	ui.Row(c).MinHeight(22).Gap(10).PaddingX(16).AlignItems(ui.Center).Children(func() {
		ui.Text(c, l.Time.Format("15:04:05")).TextColor(timeColor).FontFeatures("tnum").Unselectable()
		ui.Text(c, levelName(l.Level)).Width(28).TextColor(color).Unselectable()
		env.Highlight(c, l.Payload, strings.TrimSpace(p.query), color).Grow(1).SingleLine()
	})
}

// Topics are the service topics this page's view reads; the shell
// rebuilds it only for these (and its own).
func (p *Page) Topics() []app.Topic {
	return []app.Topic{app.TopicLogs}
}
