// Package subs is the 订阅 page (docs/design.md §5.3): the subscription
// table, import (URL, file, drag & drop), update, edit and delete.
package subs

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// Page holds page-local UI state.
type Page struct {
	e *env.Env

	list ui.ListState
	sel  int
	sort ui.SortOrder // column the table is sorted by; zero keeps the store's order

	imp  importDialog
	edit editDialog
	raw  rawDialog

	mu        sync.Mutex
	switching string // subscription being switched to
	updating  bool   // 全部更新 running

	// async runs fn off the UI thread; tests make it synchronous.
	async func(fn func())
	// pickFile opens the system file dialog; tests replace it.
	pickFile func() (string, bool)
}

// New returns the page.
func New(e *env.Env) *Page {
	p := &Page{e: e, sel: -1}
	p.list.Sort = &p.sort
	p.async = func(fn func()) { go fn() }
	p.pickFile = func() (string, bool) {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title:   i18n.T("从文件导入订阅"),
			Filters: []mygo.FileFilter{{Name: i18n.T("YAML 配置"), Extensions: []string{"yaml", "yml"}}},
		})
		if err != nil || len(paths) == 0 {
			return "", false
		}
		return paths[0], true
	}
	return p
}

// SetAsync replaces how background work runs (tests run it inline) and
// how a file is picked.
func (p *Page) SetAsync(async func(fn func()), pickFile func() (string, bool)) {
	if async != nil {
		p.async = async
	}
	if pickFile != nil {
		p.pickFile = pickFile
	}
}

// View draws the whole content area including the header.
func (p *Page) View(c *ui.Context) {
	t := c.Theme()
	k := p.e.Kit
	subs := p.e.API.Subscriptions()
	p.mu.Lock()
	switching, updating := p.switching, p.updating
	p.mu.Unlock()
	switch arg := p.e.Arg(env.Subs); arg {
	case "import-file":
		p.importFile()
	default:
		// "import-url", or a deep link's "import-url:<url>[\n<name>]": the
		// dialog opens prefilled; nothing imports by itself (docs/design.md §6.1).
		if u, name, ok := env.ParseImportURL(arg); ok {
			p.openImport(c)
			if u != "" {
				p.imp.url, p.imp.name = u, name
			}
		}
	}

	k.Header(c, i18n.T("订阅"), "", func() {
		tip := i18n.T("更新所有链接订阅（⌘R）")
		if !p.e.Mac {
			tip = i18n.T("更新所有链接订阅（Ctrl+R）")
		}
		b := ui.Button(c, "").Label(i18n.T("全部更新")).Tooltip(tip).Disabled(len(subs) == 0 || updating)
		b.Children(func() {
			if updating {
				ui.Spinner(c).Size(12, 12)
			} else {
				k.Icon(c, "refresh", 13)
			}
			ui.Text(c, i18n.T("全部更新")).SingleLine()
		})
		if b.Clicked() {
			p.updateAll()
		}
		ui.MenuButton(c, i18n.T("导入"), func(m *ui.Menu) {
			if m.Item(i18n.T("从链接导入…")).Chosen() {
				p.openImport(c)
			}
			if m.Item(i18n.T("从文件导入…")).Chosen() {
				p.importFile()
			}
		}).Label(i18n.T("导入"))
	})
	if len(subs) > 0 && c.Shortcut(ui.Cmd, ui.KeyR) {
		p.updateAll()
	}

	if len(subs) == 0 {
		k.Empty(c, "subs", i18n.T("还没有订阅"), i18n.T("也可以把 .yaml / .yml 文件拖到这个窗口。"), func() {
			if ui.PrimaryButton(c, i18n.T("从链接导入")).Clicked() {
				p.openImport(c)
			}
			if ui.Button(c, i18n.T("从文件导入")).Clicked() {
				p.importFile()
			}
		})
		p.dialogs(c)
		return
	}

	subs = sortSubs(subs, p.sort)
	ui.Column(c).Grow(1).Padding(0, 24, 16, 24).Gap(8).Children(func() {
		p.list.Selected = &p.sel
		p.list.RoundRows = true
		p.list.Key = func(i int) any {
			if i < len(subs) {
				return subs[i].ID
			}
			return i
		}
		p.list.Label = func(i int) string {
			if i < len(subs) {
				return subs[i].Name
			}
			return ""
		}
		tbl := ui.Table(c, &p.list, env.I18nColumns(columns), len(subs), func(row, col int) {
			if row >= len(subs) {
				return
			}
			p.cell(c, subs[row], columns[col].ID, switching)
		}).Grow(1).Label(i18n.T("订阅"))
		if tbl.Submitted() && p.sel >= 0 && p.sel < len(subs) && !subs[p.sel].Current {
			p.use(subs[p.sel])
		}
		ui.Text(c, i18n.T("双击切换订阅 · 可将 YAML 文件拖到此处导入")).FontSize(11).TextColor(t.TextMuted).TextAlign(ui.Center)
	})
	p.dialogs(c)
}

// columns of the subscription table (docs/design.md §5.3); every data
// column sorts.
var columns = []ui.TableColumn{
	{Title: "名称", ID: "name", MinWidth: 160, Sortable: true},
	{Title: "来源", ID: "source", Width: 64, Sortable: true},
	{Title: "已用 / 总量", ID: "usage", Width: 160, Sortable: true},
	{Title: "到期", ID: "expire", Width: 100, Sortable: true},
	{Title: "更新于", ID: "updated", Width: 136, Sortable: true},
	{Title: "", ID: "menu", Width: 56, Fixed: true},
}

// sortSubs returns the subscriptions in the table's order: the store's
// when no column is chosen, else by that column with the current one
// first on ties. Unknown values (no quota, no expiry) sort last.
func sortSubs(subs []model.Subscription, s ui.SortOrder) []model.Subscription {
	out := append([]model.Subscription(nil), subs...)
	if s.Column == "" {
		return out
	}
	// unknown reports whether the column's value is missing for s.
	unknown := func(x model.Subscription) bool {
		switch s.Column {
		case "usage":
			return x.Total <= 0
		case "expire":
			return x.Expire.IsZero()
		case "updated":
			return x.UpdatedAt.IsZero()
		}
		return false
	}
	// less orders a before b ascending; ok=false when they tie.
	less := func(a, b model.Subscription) (lt, ok bool) {
		switch s.Column {
		case "name":
			return a.Name < b.Name, a.Name != b.Name
		case "source":
			return a.Source < b.Source, a.Source != b.Source
		case "usage":
			fa, fb := usedFraction(a), usedFraction(b)
			return fa < fb, fa != fb
		case "expire":
			return a.Expire.Before(b.Expire), !a.Expire.Equal(b.Expire)
		default: // "updated"
			return a.UpdatedAt.Before(b.UpdatedAt), !a.UpdatedAt.Equal(b.UpdatedAt)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ua, ub := unknown(a), unknown(b); ua != ub {
			return ub // unknown values last, whatever the direction
		}
		if s.Descending {
			a, b = b, a
		}
		if lt, ok := less(a, b); ok {
			return lt
		}
		return out[i].Current && !out[j].Current
	})
	return out
}

// usedFraction is the share of the quota used.
func usedFraction(s model.Subscription) float64 {
	if s.Total <= 0 {
		return 0
	}
	return float64(s.Upload+s.Download) / float64(s.Total)
}

// cell builds one table cell; col is the column's ID.
func (p *Page) cell(c *ui.Context, s model.Subscription, col string, switching string) {
	t := c.Theme()
	k := p.e.Kit
	switch col {
	case "name":
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(4, 0).Children(func() {
			if s.Current {
				k.Dot(c, "accent")
			} else {
				ui.Box(c).Size(8, 8)
			}
			name := ui.Text(c, s.Name).FontSize(13).SingleLine()
			if s.Current {
				name.FontWeight(600)
			}
			if s.Current {
				k.Tag(c, i18n.T("当前"), "accent")
			}
		})
	case "source":
		src := i18n.T("文件")
		if s.Source == "url" {
			src = i18n.T("链接")
		}
		ui.Text(c, src).FontSize(12).TextColor(t.TextMuted)
	case "usage":
		if s.Total <= 0 {
			ui.Text(c, "—").FontSize(12).TextColor(t.TextMuted)
			return
		}
		used := s.Upload + s.Download
		frac := float64(used) / float64(s.Total)
		ui.Column(c).Gap(3).Padding(3, 0).Children(func() {
			ui.Text(c, fmt.Sprintf("%s / %s GB", env.FmtGB(used), env.FmtGB(s.Total))).FontSize(11).FontFeatures("tnum").SingleLine()
			bar := ui.Progress(c, min(1, frac)).Width(120).Label(i18n.T("已用流量"))
			if frac > 0.85 {
				bar.TextColor(t.Warning)
			}
		})
	case "expire":
		switch {
		case s.Expire.IsZero():
			ui.Text(c, "—").FontSize(12).TextColor(t.TextMuted)
		case s.Expire.Before(time.Now()):
			ui.Text(c, i18n.T("已过期")).FontSize(12).TextColor(t.Danger)
		case time.Until(s.Expire) < 7*24*time.Hour:
			ui.Text(c, env.FmtDate(s.Expire)).FontSize(12).TextColor(t.Warning).FontFeatures("tnum")
		default:
			ui.Text(c, env.FmtDate(s.Expire)).FontSize(12).FontFeatures("tnum")
		}
	case "updated":
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			switch {
			case s.Updating:
				ui.Spinner(c).Size(12, 12).Label(i18n.T("更新中"))
				ui.Text(c, i18n.T("更新中…")).FontSize(12).TextColor(t.TextMuted)
			case switching == s.ID:
				ui.Spinner(c).Size(12, 12).Label(i18n.T("切换中"))
				ui.Text(c, i18n.T("切换中…")).FontSize(12).TextColor(t.TextMuted)
			default:
				ui.Text(c, env.FmtAgo(s.UpdatedAt)).FontSize(12).TextColor(t.TextMuted).FontFeatures("tnum")
				if s.LastError != "" {
					ui.Box(c).TextColor(t.Warning).Cursor(ui.CursorHelp).Tooltip(s.LastError).Label(i18n.T("更新失败")).Children(func() {
						k.Icon(c, "warn", 13)
					})
				}
			}
		})
	case "menu":
		ui.Button(c, "").Label(i18n.Tf("更多 %s", s.Name)).Tooltip(i18n.T("更多")).Children(func() { k.Icon(c, "more", 14) }).Menu(func(m *ui.Menu) {
			p.rowMenu(c, m, s)
		})
	}
}

// rowMenu is the ⋯ menu of a row.
func (p *Page) rowMenu(c *ui.Context, m *ui.Menu, s model.Subscription) {
	sub := s
	if m.Item(i18n.T("使用此订阅")).Disabled(sub.Current).Chosen() {
		p.use(sub)
	}
	update := i18n.T("更新")
	if sub.Source != "url" {
		update = i18n.T("更新（重新读取文件）")
	}
	if m.Item(update).Disabled(sub.Updating).Chosen() {
		p.update(sub)
	}
	if m.Item(i18n.T("编辑…")).Chosen() {
		p.edit = editDialog{open: true, id: sub.ID, name: sub.Name, url: sub.URL, isURL: sub.Source == "url",
			auto: autoIndex(sub.AutoUpdateHours), ua: sub.UserAgent, viaProxy: sub.ViaProxy, timeout: timeoutText(sub.TimeoutSec)}
	}
	if m.Item(i18n.T("编辑覆写…")).Chosen() {
		p.e.Navigate(env.Settings, "override:"+sub.ID)
	}
	if m.Item(i18n.T("查看配置")).Chosen() {
		p.openRaw(sub)
	}
	m.Separator()
	if m.Item(i18n.T("复制链接")).Disabled(sub.Source != "url").Chosen() {
		c.WriteClipboard(sub.URL)
		p.e.Toast(i18n.T("已复制"), false)
	}
	if m.Item(i18n.T("打开官网")).Disabled(sub.HomePage == "").Tooltip(sub.HomePage).Chosen() {
		_ = mygo.Shell.OpenExternal(sub.HomePage)
	}
	show := i18n.T("在访达中显示")
	if !p.e.Mac {
		show = i18n.T("在资源管理器中显示")
	}
	if m.Item(show).Disabled(sub.FilePath == "").Chosen() {
		mygo.Shell.ShowItemInFolder(sub.FilePath)
	}
	m.Separator()
	del := m.Item(i18n.T("删除…")).Danger().Disabled(sub.Current)
	if sub.Current {
		del.Tooltip(i18n.T("请先切换到其他订阅"))
	}
	if del.Chosen() {
		p.askDelete(sub)
	}
}

// AskDelete asks to delete the subscription with that ID (the ⋯ menu's 删除…).
func (p *Page) AskDelete(id string) {
	for _, s := range p.e.API.Subscriptions() {
		if s.ID == id {
			p.askDelete(s)
			return
		}
	}
}

// askDelete confirms, then deletes.
func (p *Page) askDelete(s model.Subscription) {
	p.e.Confirm(i18n.Tf("删除「%s」？", s.Name), i18n.T("此操作无法撤销。"), i18n.T("删除"), true, func() {
		p.e.Run(func() error { return p.e.API.DeleteSubscription(s.ID) }, i18n.Tf("已删除「%s」", s.Name))
	})
}

// update re-downloads a link subscription or re-reads a file one. A
// validation failure is reported by the service as the §6.3 dialog, so it
// is not toasted again.
func (p *Page) update(s model.Subscription) {
	ok := i18n.Tf("已更新「%s」", s.Name)
	if s.Source != "url" {
		ok = i18n.Tf("已重新读取「%s」", s.Name)
	}
	p.e.Run(func() error {
		err := p.e.API.UpdateSubscription(s.ID)
		switch {
		case err == nil:
			p.e.Toast(ok, false)
		case errors.Is(err, app.ErrInvalidConfig):
			return nil
		}
		return err
	}, "")
}

// use switches to a subscription, showing a spinner on its row meanwhile.
func (p *Page) use(s model.Subscription) {
	p.mu.Lock()
	p.switching = s.ID
	p.mu.Unlock()
	p.e.Run(func() error {
		err := p.e.API.UseSubscription(s.ID)
		p.mu.Lock()
		p.switching = ""
		p.mu.Unlock()
		return err
	}, i18n.Tf("已切换到 %s", s.Name))
}

func (p *Page) updateAll() {
	p.mu.Lock()
	if p.updating {
		p.mu.Unlock()
		return
	}
	p.updating = true
	p.mu.Unlock()
	p.e.Run(func() error {
		err := p.e.API.UpdateAllSubscriptions()
		p.mu.Lock()
		p.updating = false
		p.mu.Unlock()
		return err
	}, i18n.T("订阅已更新"))
}

// importFile picks a YAML file and imports it.
func (p *Page) importFile() {
	first := !p.e.API.Status().HasSubscriptions
	p.async(func() {
		path, ok := p.pickFile()
		if !ok {
			return
		}
		p.e.Run(func() error { return ImportFile(p.e.API, path, first) }, ImportToast(path, first))
	})
}

// ImportFile imports a YAML file as a subscription. The first subscription
// ever (first: there was none before) is used and the core is started
// right away (docs/design.md §8.1 首次启动); when the core then fails to
// start, the import is kept and the error says so. The shell's drag and
// drop uses it too.
func ImportFile(api app.API, path string, first bool) error {
	sub, err := api.ImportSubscriptionFile(path, first)
	if err != nil {
		return err
	}
	if first {
		if err := api.StartCore(); err != nil {
			return fmt.Errorf(i18n.T("已导入 %s，但内核启动失败：%s"), sub.Name, err)
		}
	}
	return nil
}

// ImportToast is the success message of ImportFile: 「已导入并启动」 for the
// first subscription, else 「已导入 <file>」.
func ImportToast(path string, first bool) string {
	if first {
		return i18n.T("已导入并启动")
	}
	return i18n.Tf("已导入 %s", filepath.Base(path))
}

// isURL reports whether s looks like a subscription link.
func isURL(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// Topics are the service topics this page's view reads; the shell
// rebuilds it only for these (and its own).
func (p *Page) Topics() []app.Topic {
	return []app.Topic{app.TopicStatus, app.TopicSubs}
}
