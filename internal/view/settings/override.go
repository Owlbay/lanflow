package settings

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// overrideDialog is the state of 编辑覆写 (design §6.9): the YAML editor of
// one override entry, keyed by its ID ("global" and a subscription's ID
// name the legacy entries, created when first saved).
type overrideDialog struct {
	open       bool
	key, title string
	state      *editor.State
	diff       *editor.DiffState
	showDiff   bool
	saving     bool
	previewing bool
	err        string
	saved      string // text as saved, to tell edits
}

// runtimeDialog is the state of 运行时配置 (design §6.10).
type runtimeDialog struct {
	open    bool
	loading bool
	lines   []model.RuntimeLine
	err     string
}

// overrideListUI is the state of the override list's own dialogs: naming
// (new entry / rename), import from a link, and a subscription's selection.
type overrideListUI struct {
	name struct {
		open    bool
		id      string // "" = a new entry
		text    string
		session int
	}
	link struct {
		open      bool
		name, url string
		busy      bool
		err       string
	}
	sel struct {
		open    bool
		subID   string
		subName string
		chosen  map[string]bool
	}
	busy      map[string]bool // override ID → an update / save runs
	openAfter string          // editor to open once a new entry is saved (set by jobs)
}

// effectiveLines counts the lines that are neither blank nor comments.
func effectiveLines(text string) int {
	n := 0
	for _, l := range strings.Split(text, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			n++
		}
	}
	return n
}

func linesDesc(text string) string {
	if n := effectiveLines(text); n > 0 {
		return i18n.Tf("%d 行有效内容", n)
	}
	return i18n.T("未设置")
}

// overrideMeta is the muted line under an entry's name.
func overrideMeta(o model.Override) string {
	meta := linesDesc(o.Text)
	if o.Source == model.OverrideURL {
		meta += i18n.T(" · 链接")
	}
	if !o.UpdatedAt.IsZero() {
		meta += i18n.T(" · 更新于 ") + env.FmtAgo(o.UpdatedAt)
	}
	return meta
}

func (p *Page) overrideTab(c *ui.Context, t *ui.Theme) {
	e := p.e
	list := e.API.OverrideList()
	subIDs := map[string]bool{}
	for _, s := range e.API.Subscriptions() {
		subIDs[s.ID] = true
	}
	p.mu.Lock()
	if p.ovl.openAfter != "" {
		key := p.ovl.openAfter
		p.ovl.openAfter = ""
		p.mu.Unlock()
		p.openOverride(key)
	} else {
		p.mu.Unlock()
	}
	// Two columns at ≥1100 pt: the explanation and the list on the left,
	// the subscriptions' choices and the result on the right.
	env.Columns(c, 20, func() {
		ui.Column(c).Gap(6).Padding(12).Radius(10).Background(t.Surface).Border(1, t.Border).Children(func() {
			ui.Text(c, i18n.T("覆写用 YAML 修改订阅配置，订阅更新后依然有效。")+i18n.T("列表中的覆写按顺序应用：全局的作用于所有订阅，其余由各订阅自行选择。"))
			ui.RichText(c,
				ui.Span{Text: i18n.T("写法："), Color: t.TextMuted, Size: 12},
				ui.Span{Text: "key", Font: "monospace", Size: 12},
				ui.Span{Text: i18n.T(" 整体替换 · "), Color: t.TextMuted, Size: 12},
				ui.Span{Text: "+key", Font: "monospace", Size: 12},
				ui.Span{Text: i18n.T(" 插到前面 · "), Color: t.TextMuted, Size: 12},
				ui.Span{Text: "key+", Font: "monospace", Size: 12},
				ui.Span{Text: i18n.T(" 追加到后面。保存前会完整校验，失败不会影响当前配置。"), Color: t.TextMuted, Size: 12},
			)
		})
		e.Kit.Section(c, i18n.T("覆写列表"), i18n.T("按从上到下的顺序应用"), func() {
			if len(list) == 0 {
				ui.Text(c, i18n.T("还没有覆写")).TextColor(t.TextMuted).Padding(10, 12)
			}
			for i, o := range list {
				p.overrideRow(c, t, o, i, len(list), subIDs[o.ID])
			}
			ui.Row(c).Gap(8).MinHeight(40).PaddingX(12).AlignItems(ui.Center).Children(func() {
				ui.Button(c, i18n.T("新建…")).OnClick(func() { p.openName("", "") })
				ui.Button(c, i18n.T("从文件导入…")).OnClick(func() { go p.importOverrideFile() })
				ui.Button(c, i18n.T("从链接导入…")).OnClick(func() {
					p.ovl.link.open, p.ovl.link.name, p.ovl.link.url, p.ovl.link.err = true, "", "", ""
				})
			})
		})
	}, func() {
		e.Kit.Section(c, i18n.T("订阅使用的覆写"), i18n.T("每个订阅可以额外选择要应用的覆写；全局覆写始终应用"), func() {
			subs := e.API.Subscriptions()
			if len(subs) == 0 {
				ui.Text(c, i18n.T("还没有订阅")).TextColor(t.TextMuted).Padding(10, 12)
			}
			byID := map[string]model.Override{}
			for _, o := range list {
				byID[o.ID] = o
			}
			for _, s := range subs {
				s := s
				ui.Row(c.Key(s.ID)).Gap(10).MinHeight(40).PaddingX(12).AlignItems(ui.Center).Children(func() {
					ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
						ui.Row(c).Gap(6).Children(func() {
							ui.Text(c, s.Name)
							if s.Current {
								e.Kit.Tag(c, i18n.T("当前"), "accent")
							}
						})
						ui.Text(c, selectionDesc(s, byID)).FontSize(11).TextColor(t.TextMuted).SingleLine()
					})
					ui.Button(c, i18n.T("选择…")).Label(i18n.T("选择…") + " " + s.Name).OnClick(func() { p.openSelection(s, list) })
				})
			}
		})
		e.Kit.Section(c, i18n.T("合成结果"), "", func() {
			e.Kit.Row(c, i18n.T("运行时配置"), i18n.T("订阅 → 订阅覆写 → 全局覆写 → 澜流设置，最终交给内核的配置，每段标注来源"), func() {
				ui.Button(c, i18n.T("查看…")).OnClick(p.openRuntime)
			})
		})
	})
}

// selectionDesc names the overrides a subscription applies.
func selectionDesc(s model.Subscription, byID map[string]model.Override) string {
	var names []string
	for _, id := range s.Overrides {
		if o, ok := byID[id]; ok {
			names = append(names, o.Name)
		}
	}
	if len(names) == 0 {
		return i18n.T("仅全局覆写")
	}
	return i18n.T("全局覆写 + ") + strings.Join(names, i18n.T("、"))
}

// overrideRow draws one entry of the list with its switch, editor button
// and ⋯ menu.
func (p *Page) overrideRow(c *ui.Context, t *ui.Theme, o model.Override, index, count int, subOwn bool) {
	e := p.e
	id := o.ID
	p.mu.Lock()
	busy := p.ovl.busy[id]
	p.mu.Unlock()
	ui.Row(c.Key("ov-" + id)).Gap(10).MinHeight(44).PaddingX(12).AlignItems(ui.Center).Children(func() {
		on := o.Enabled
		if ui.Switch(c.Key("on"), &on).Label(i18n.T("启用 ") + o.Name).Disabled(busy).Changed() {
			n := o
			n.Enabled = on
			p.saveEntry(n, "")
		}
		ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, o.Name).SingleLine().Shrink(1)
				if o.Global {
					e.Kit.Tag(c, i18n.T("全局"), "accent")
				}
				if subOwn {
					// The entry a subscription's 编辑覆写… opens (its former single text).
					ui.Box(c).Tooltip(i18n.T("只作用于对应订阅，先于全局覆写应用")).Children(func() { e.Kit.Tag(c, i18n.T("订阅覆写"), "gray") })
				}
				if busy {
					ui.Spinner(c).Size(12, 12).Label(i18n.T("更新中"))
				}
			})
			ui.Text(c, overrideMeta(o)).FontSize(11).TextColor(t.TextMuted).SingleLine()
		})
		ui.Button(c, i18n.T("编辑…")).OnClick(func() { p.openOverride(id) })
		ui.Button(c, "").Label(i18n.T("更多 ") + o.Name).Tooltip(i18n.T("更多")).Children(func() { e.Kit.Icon(c, "more", 14) }).Menu(func(m *ui.Menu) {
			if m.Item(i18n.T("重命名…")).Chosen() {
				p.openName(id, o.Name)
			}
			if m.Item(i18n.T("作用于所有订阅")).Checked(o.Global).Disabled(id == "global").Chosen() {
				n := o
				n.Global = !o.Global
				p.saveEntry(n, "")
			}
			m.Separator()
			if m.Item(i18n.T("上移")).Disabled(index == 0).Chosen() {
				e.Run(func() error { return e.API.MoveOverrideEntry(id, -1) }, "")
			}
			if m.Item(i18n.T("下移")).Disabled(index >= count-1).Chosen() {
				e.Run(func() error { return e.API.MoveOverrideEntry(id, 1) }, "")
			}
			if o.Source == model.OverrideURL {
				m.Separator()
				if m.Item(i18n.T("更新")).Disabled(busy).Chosen() {
					p.setBusy(id, true)
					e.Run(func() error {
						defer p.setBusy(id, false)
						return e.API.UpdateOverrideEntry(id)
					}, i18n.Tf("已更新「%s」", o.Name))
				}
				if m.Item(i18n.T("复制链接")).Chosen() {
					c.WriteClipboard(o.URL)
					e.Toast(i18n.T("已复制"), false)
				}
			}
			m.Separator()
			if m.Item(i18n.T("删除…")).Danger().Chosen() {
				name := o.Name
				e.Confirm(i18n.Tf("删除覆写「%s」？", name), i18n.T("使用它的订阅会重新生成配置。此操作无法撤销。"), i18n.T("删除"), true, func() {
					e.Run(func() error { return e.API.DeleteOverrideEntry(id) }, i18n.Tf("已删除「%s」", name))
				})
			}
		})
	})
}

func (p *Page) setBusy(id string, on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ovl.busy == nil {
		p.ovl.busy = map[string]bool{}
	}
	if on {
		p.ovl.busy[id] = true
	} else {
		delete(p.ovl.busy, id)
	}
}

// saveEntry saves an entry off the UI thread; a new one (empty ID) opens
// its editor afterwards.
func (p *Page) saveEntry(o model.Override, ok string) {
	e := p.e
	if o.ID != "" {
		p.setBusy(o.ID, true)
	}
	e.Run(func() error {
		saved, err := e.API.SaveOverrideEntry(o)
		if o.ID != "" {
			p.setBusy(o.ID, false)
		}
		if err != nil {
			return err
		}
		if o.ID == "" && o.Source != model.OverrideURL {
			p.mu.Lock()
			p.ovl.openAfter = saved.ID
			p.mu.Unlock()
		}
		return nil
	}, ok)
}

// openName opens the naming dialog: a new entry when id is "".
func (p *Page) openName(id, name string) {
	d := &p.ovl.name
	d.open, d.id, d.text = true, id, name
	d.session++
}

// importOverrideFile picks a YAML file and adds it as a local entry. The
// system dialog blocks, so it runs on its own goroutine.
func (p *Page) importOverrideFile() {
	e := p.e
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title:   i18n.T("从文件导入覆写"),
		Filters: []mygo.FileFilter{{Name: i18n.T("YAML 配置"), Extensions: []string{"yaml", "yml"}}},
	})
	if err != nil || len(paths) == 0 {
		return
	}
	path := paths[0]
	e.Run(func() error {
		data, err := os.ReadFile(path)
		if err != nil {
			return i18n.Error(i18n.Tf("读取文件失败：%v", err))
		}
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		_, err = e.API.SaveOverrideEntry(model.Override{Name: name, Enabled: true, Source: model.OverrideLocal, Text: string(data)})
		return err
	}, i18n.T("已导入 ")+filepath.Base(path))
}

// openSelection opens a subscription's override selection.
func (p *Page) openSelection(s model.Subscription, list []model.Override) {
	d := &p.ovl.sel
	d.open, d.subID, d.subName = true, s.ID, s.Name
	d.chosen = map[string]bool{}
	for _, id := range s.Overrides {
		d.chosen[id] = true
	}
}

// overrideListDialogs draws the naming, link import and selection dialogs.
func (p *Page) overrideListDialogs(c *ui.Context, t *ui.Theme) {
	e := p.e
	if d := &p.ovl.name; d.open {
		ui.Modal(c, &d.open, func() {
			ui.Column(c.Key(d.session)).Width(380).Gap(10).Children(func() {
				title := i18n.T("新建覆写")
				if d.id != "" {
					title = i18n.T("重命名覆写")
				}
				ui.Text(c, title).FontSize(15).Bold()
				in := ui.TextInput(c.Key("name"), &d.text).Placeholder(i18n.T("覆写名称")).Label(i18n.T("覆写名称")).AutoFocus()
				save := func() {
					name := strings.TrimSpace(d.text)
					if name == "" {
						return
					}
					d.open = false
					if d.id == "" {
						p.saveEntry(model.Override{Name: name, Enabled: true, Source: model.OverrideLocal}, "")
						return
					}
					for _, o := range e.API.OverrideList() {
						if o.ID == d.id {
							o.Name = name
							p.saveEntry(o, i18n.T("已重命名"))
						}
					}
				}
				if in.Submitted() {
					save()
				}
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, i18n.T("取消")).Clicked() {
						d.open = false
					}
					label := i18n.T("创建")
					if d.id != "" {
						label = i18n.T("保存")
					}
					ui.PrimaryButton(c, label).Disabled(strings.TrimSpace(d.text) == "").OnClick(save)
				})
			})
		})
	}
	if d := &p.ovl.link; d.open {
		md := ui.Modal(c, &d.open, func() {
			ui.Column(c.Key("ov-link")).Width(420).Gap(10).Children(func() {
				ui.Text(c, i18n.T("从链接导入覆写")).FontSize(15).Bold()
				ui.Text(c, i18n.T("下载链接指向的 YAML 作为一条覆写；之后可在它的菜单中「更新」。")).FontSize(12).TextColor(t.TextMuted)
				ui.TextInput(c.Key("url"), &d.url).Placeholder("https://").Label(i18n.T("覆写链接")).AutoFocus().ReadOnly(d.busy).Invalid(d.err != "")
				ui.TextInput(c.Key("name"), &d.name).Placeholder(i18n.T("名称（留空则用文件名）")).Label(i18n.T("覆写名称")).ReadOnly(d.busy)
				if d.err != "" {
					ui.Text(c, d.err).FontSize(12).TextColor(t.Danger).Role(ui.RoleAlert)
				}
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					ui.Button(c, i18n.T("取消")).Disabled(d.busy).OnClick(func() { d.open = false })
					label := i18n.T("导入")
					if d.busy {
						label = i18n.T("正在下载…")
					}
					ui.PrimaryButton(c, label).Disabled(d.busy).OnClick(p.importOverrideLink)
				})
			})
		})
		if md.Dismissed() && d.busy {
			d.open = true
		}
	}
	if d := &p.ovl.sel; d.open {
		list := e.API.OverrideList()
		ui.Modal(c, &d.open, func() {
			ui.Column(c.Key("ov-sel-" + d.subID)).Width(420).Gap(10).Children(func() {
				ui.Text(c, i18n.T("选择覆写 · ")+d.subName).FontSize(15).Bold()
				ui.Text(c, i18n.T("勾选的覆写按列表顺序应用于这个订阅；全局覆写始终应用。")).FontSize(12).TextColor(t.TextMuted)
				if len(list) == 0 {
					ui.Text(c, i18n.T("还没有覆写")).TextColor(t.TextMuted)
				}
				for _, o := range list {
					o := o
					if o.Global {
						on := true
						ui.Checkbox(c.Key(o.ID), &on, o.Name+i18n.T("（全局）")).Disabled(true)
						continue
					}
					on := d.chosen[o.ID]
					if ui.Checkbox(c.Key(o.ID), &on, o.Name).Changed() {
						d.chosen[o.ID] = on
					}
				}
				ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
					if ui.Button(c, i18n.T("取消")).Clicked() {
						d.open = false
					}
					ui.PrimaryButton(c, i18n.T("保存")).OnClick(func() {
						var ids []string
						for _, o := range list {
							if !o.Global && d.chosen[o.ID] {
								ids = append(ids, o.ID)
							}
						}
						d.open = false
						for _, s := range e.API.Subscriptions() {
							if s.ID == d.subID {
								s.Overrides = ids
								e.Run(func() error { return e.API.EditSubscription(s) }, i18n.T("已保存"))
							}
						}
					})
				})
			})
		})
	}
}

// importOverrideLink downloads the link of the import dialog.
func (p *Page) importOverrideLink() {
	e := p.e
	d := &p.ovl.link
	u := strings.TrimSpace(d.url)
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		d.err = i18n.T("请输入以 http:// 或 https:// 开头的链接。")
		return
	}
	name := strings.TrimSpace(d.name)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(u), filepath.Ext(u))
		if i := strings.IndexAny(name, "?#"); i >= 0 {
			name = name[:i]
		}
		if name == "" || name == "." || name == "/" {
			name = u
		}
	}
	d.busy, d.err = true, ""
	e.Run(func() error {
		_, err := e.API.SaveOverrideEntry(model.Override{Name: name, Enabled: true, Source: model.OverrideURL, URL: u})
		p.mu.Lock()
		defer p.mu.Unlock()
		d.busy = false
		if err != nil {
			d.err = err.Error()
			return nil // shown inline
		}
		d.open = false
		p.toast = i18n.Tf("已导入「%s」", name)
		return nil
	}, "")
}

// openOverride opens the editor for key: an override ID, "global" (the
// global entry) or a subscription ID (its own entry).
func (p *Page) openOverride(key string) {
	e := p.e
	var text string
	title := i18n.T("编辑覆写")
	found := false
	for _, o := range e.API.OverrideList() {
		if o.ID == key {
			text, found = o.Text, true
			if key != "global" {
				title = i18n.T("编辑覆写 · ") + o.Name
			}
		}
	}
	if key == "global" {
		title = i18n.T("编辑全局覆写")
	} else if !found {
		for _, s := range e.API.Subscriptions() {
			if s.ID == key {
				title = i18n.T("编辑覆写 · ") + s.Name
			}
		}
	}
	buf := editor.NewBuffer(text)
	buf.SetLanguage(yaml)
	p.ov = overrideDialog{open: true, key: key, title: title, saved: text,
		state: &editor.State{Buffer: buf, SoftWrap: true, Label: i18n.T("覆写 YAML"), Placeholder: i18n.T("# 在这里写 YAML 覆写，例如\n# log-level: warning\n# +rules:\n#   - DOMAIN-SUFFIX,example.com,DIRECT")}}
}

// OverrideText returns the editor's current text (for tests).
func (p *Page) OverrideText() string {
	if p.ov.state == nil {
		return ""
	}
	return p.ov.state.Buffer.String()
}

// SetOverrideText replaces the editor's text (for tests).
func (p *Page) SetOverrideText(s string) {
	if p.ov.state != nil {
		p.ov.state.Buffer.Set(s)
	}
}

func (p *Page) overrideEditor(c *ui.Context, t *ui.Theme) {
	d := &p.ov
	if !d.open {
		return
	}
	modified := d.state.Buffer.String() != d.saved
	dlg := ui.Modal(c, &d.open, func() {
		ui.Column(c.Key("ov-" + d.key)).Width(580).Gap(10).Children(func() {
			ui.Text(c, d.title).FontSize(15).Bold()
			ui.RichText(c,
				ui.Span{Text: "key", Font: "monospace", Size: 12},
				ui.Span{Text: i18n.T(" 整体替换 · "), Color: t.TextMuted, Size: 12},
				ui.Span{Text: "+key", Font: "monospace", Size: 12},
				ui.Span{Text: i18n.T(" 插到前面 · "), Color: t.TextMuted, Size: 12},
				ui.Span{Text: "key+", Font: "monospace", Size: 12},
				ui.Span{Text: i18n.T(" 追加到后面"), Color: t.TextMuted, Size: 12},
			)
			if d.showDiff && d.diff != nil {
				editor.DiffView(c, d.diff).Height(320).FillWidth().Radius(6).Border(1, t.Border)
			} else {
				d.state.ReadOnly = d.saving
				editor.View(c, d.state).Height(320).FillWidth().Radius(6).Border(1, t.Border)
			}
			if d.err != "" {
				ui.Text(c, d.err).FontSize(12).TextColor(t.Danger).Selectable()
			}
			ui.Row(c).Gap(8).Children(func() {
				switch {
				case d.showDiff:
					if ui.Button(c, i18n.T("返回编辑")).Clicked() {
						d.showDiff = false
					}
				case d.previewing:
					ui.Row(c).Gap(6).Children(func() {
						ui.Spinner(c)
						ui.Text(c, i18n.T("生成差异…")).TextColor(t.TextMuted)
					})
				default:
					ui.Button(c, i18n.T("预览差异")).Disabled(d.saving).OnClick(p.previewOverride)
				}
				ui.Spacer(c)
				ui.Button(c, i18n.T("取消")).Disabled(d.saving).OnClick(func() { p.closeOverride(modified) })
				label := i18n.T("保存")
				if d.saving {
					label = i18n.T("校验中…")
				}
				ui.PrimaryButton(c, label).Disabled(d.saving || d.previewing).OnClick(p.saveOverride)
			})
		})
	})
	if dlg.Dismissed() && (modified || d.saving) {
		d.open = true
		if !d.saving {
			p.closeOverride(true)
		}
	}
}

func (p *Page) closeOverride(modified bool) {
	if !modified {
		p.ov.open = false
		return
	}
	p.e.Confirm(i18n.T("放弃修改？"), i18n.T("覆写内容已修改但尚未保存。"), i18n.T("放弃"), true, func() { p.ov.open = false })
}

func (p *Page) previewOverride() {
	e := p.e
	d := &p.ov
	if d.previewing {
		return
	}
	key, text := d.key, d.state.Buffer.String()
	d.previewing, d.err = true, ""
	e.Run(func() error {
		lines, err := e.API.PreviewOverride(key, text)
		p.mu.Lock()
		defer p.mu.Unlock()
		d.previewing = false
		if err != nil {
			d.err = err.Error()
			return nil
		}
		d.diff = diffOf(lines)
		d.showDiff = true
		return nil
	}, "")
}

// diffOf turns preview lines into a unified diff view: the old text is
// the lines kept and removed, the new text the lines kept and added.
func diffOf(lines []model.DiffLine) *editor.DiffState {
	var oldText, newText strings.Builder
	for _, l := range lines {
		switch l.Op {
		case '+':
			newText.WriteString(l.Text + "\n")
		case '-':
			oldText.WriteString(l.Text + "\n")
		default:
			oldText.WriteString(l.Text + "\n")
			newText.WriteString(l.Text + "\n")
		}
	}
	o, n := editor.NewBuffer(oldText.String()), editor.NewBuffer(newText.String())
	o.SetLanguage(yaml)
	n.SetLanguage(yaml)
	return &editor.DiffState{Old: o, New: n, Layout: editor.DiffUnified, ExpandUnchanged: true, Wrap: true, Indicators: editor.DiffClassic}
}

func (p *Page) saveOverride() {
	e := p.e
	d := &p.ov
	if d.saving {
		return
	}
	key, text := d.key, d.state.Buffer.String()
	d.saving, d.err = true, ""
	e.Run(func() error {
		err := e.API.SaveOverride(key, text)
		p.mu.Lock()
		defer p.mu.Unlock()
		d.saving = false
		if err != nil {
			d.err = err.Error()
			return nil // shown inline; the current config is unchanged
		}
		d.saved = text
		d.open = false
		p.toast = i18n.T("覆写已保存，配置已重新加载")
		return nil
	}, "")
}

// ---- 运行时配置 (§6.10) ----

func (p *Page) openRuntime() {
	e := p.e
	p.rt = runtimeDialog{open: true, loading: true}
	e.Run(func() error {
		lines, err := e.API.RuntimeConfig()
		p.mu.Lock()
		defer p.mu.Unlock()
		p.rt.loading = false
		if err != nil {
			p.rt.err = err.Error()
			return nil
		}
		p.rt.lines = lines
		return nil
	}, "")
}

func (p *Page) runtimeDialog(c *ui.Context, t *ui.Theme) {
	e := p.e
	d := &p.rt
	if !d.open {
		return
	}
	ui.Modal(c, &d.open, func() {
		ui.Column(c.Key("runtime")).Width(640).Gap(10).Children(func() {
			ui.Text(c, i18n.T("运行时配置")).FontSize(15).Bold()
			ui.Text(c, i18n.T("最终交给内核的配置（只读）。右侧灰字标注每段的来源。")).FontSize(12).TextColor(t.TextMuted)
			ui.Scroll(c).Height(380).Padding(8, 10).Radius(6).Background(t.Surface).Border(1, t.Border).Selectable().KeepScrollbars(true).Children(func() {
				switch {
				case d.loading:
					ui.Row(c).Gap(6).Children(func() {
						ui.Spinner(c)
						ui.Text(c, i18n.T("正在合成…")).TextColor(t.TextMuted)
					})
				case d.err != "":
					ui.Text(c, d.err).TextColor(t.Danger)
				default:
					for i, l := range d.lines {
						ui.Row(c.Key(i)).Gap(12).MinHeight(20).Children(func() {
							ui.Text(c, l.Text).Font("monospace").FontSize(12).NoWrap().Grow(1).PreserveSpaces()
							if l.Source != "" {
								ui.Text(c, l.Source).FontSize(11).TextColor(t.TextMuted).Unselectable()
							}
						})
					}
				}
			})
			ui.Row(c).Gap(8).Children(func() {
				ui.Button(c, i18n.T("复制")).Disabled(len(d.lines) == 0).OnClick(func() {
					var b strings.Builder
					for _, l := range d.lines {
						b.WriteString(l.Text + "\n")
					}
					c.WriteClipboard(b.String())
					e.Toast(i18n.T("已复制"), false)
				})
				ui.Spacer(c)
				if ui.PrimaryButton(c, i18n.T("关闭")).Clicked() {
					d.open = false
				}
			})
		})
	})
}
