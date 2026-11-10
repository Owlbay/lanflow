// Package rules is the 规则 page (docs/design.md §5.6): custom rules, the
// subscription's rules with hit counts, rule providers, and the add / test
// dialogs (§6.7, §6.8).
package rules

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"io"
	"sort"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/rules/ruleform"
)

// Page holds page-local UI state.
type Page struct {
	e *env.Env

	query  string
	search ui.Handle // the search field, focused by ⌘F
	filter int       // 0 全部, 1 自定义, 2 订阅
	sort   ui.SortOrder
	table  ui.ListState
	form   ruleform.Form

	// 测试… dialog
	testOpen bool
	testHost string
	testing  bool

	// sub caches which subscription rules match the query, in the order
	// shown; see subRows.
	sub subCache
	// customList is the virtual list of a long custom rule list.
	customList ui.ListState

	mu         sync.Mutex // guards the fields below, written by jobs
	testResult *model.RuleMatch
	testErr    string
}

// New returns the page.
func New(e *env.Env) *Page {
	p := &Page{e: e, sort: ui.SortOrder{Column: "#"}}
	p.table.Sort = &p.sort
	return p
}

var columns = []ui.TableColumn{
	{Title: "#", ID: "#", Width: 44, Align: ui.End, Sortable: true},
	{Title: "类型", ID: "type", Width: 130},
	{Title: "内容", ID: "payload", MinWidth: 160},
	{Title: "策略", ID: "policy", Width: 110},
	{Title: "命中", ID: "hits", Width: 70, Align: ui.End, Sortable: true},
	{Title: "最近命中", ID: "at", Width: 90},
	{Title: "", ID: "action", Width: 72, Fixed: true},
}

// View draws the whole content area including the header.
func (p *Page) View(c *ui.Context) {
	e, t := p.e, c.Theme()
	st := e.API.Status()
	running := st.Core == model.CoreRunning
	ui.Column(c).Fill().Children(func() {
		e.Kit.Header(c, i18n.T("规则"), "", func() { p.tools(c, running) })
		if c.Shortcut(ui.Cmd, ui.KeyF) {
			p.search.Focus(c)
		}
		if !running {
			e.Kit.Empty(c, "rules", i18n.T("内核未启动"), i18n.T("启动内核后这里会显示当前生效的规则及命中次数。"), func() {
				ui.PrimaryButton(c, i18n.T("启动内核")).OnClick(func() { e.Run(e.API.StartCore, "") })
			})
			return
		}
		// No page scrolling (docs/design.md §4): the subscription rules
		// table takes the height left and scrolls inside; a long list
		// of custom rules scrolls in its own box.
		rules := e.API.Rules()
		ui.Column(c).Grow(1).Padding(4, 24, 20, 24).Gap(20).Clip().Children(func() {
			if p.filter != 2 {
				p.customSection(c, t, rules)
			}
			if p.filter != 1 {
				p.subSection(c, t, rules)
				p.providers(c, t)
			}
		})
	})
	p.form.View(c, e)
	p.testDialog(c, t)
}

func (p *Page) tools(c *ui.Context, running bool) {
	e := p.e
	ui.SearchField(c.Key("search"), &p.query).Placeholder(i18n.T("搜索规则")).Label(i18n.T("搜索规则")).Width(170).Bind(&p.search)
	ui.Segmented(c.Key("filter"), &p.filter, i18n.T("全部"), i18n.T("自定义"), i18n.T("订阅")).Label(i18n.T("规则来源"))
	ui.Button(c, i18n.T("测试…")).Disabled(!running).Tooltip(i18n.T("输入域名或 IP，查看会命中哪条规则")).OnClick(func() {
		p.testOpen = true
	})
	ui.PrimaryButton(c, "").Children(func() {
		e.Kit.Icon(c, "plus", 13)
		ui.Text(c, i18n.T("添加规则"))
	}).OnClick(func() {
		p.form.Open(model.CustomRule{Target: e.API.Proxies().MainGroup})
	})
}

// lowerQuery is the query as matches compares it.
func (p *Page) lowerQuery() string { return strings.ToLower(strings.TrimSpace(p.query)) }

// matches reports whether any of parts contains q, a lowerQuery.
func matches(q string, parts ...string) bool {
	if q == "" {
		return true
	}
	for _, s := range parts {
		if strings.Contains(strings.ToLower(s), q) {
			return true
		}
	}
	return false
}

// ---- custom rules ----

// bigCustom is the custom rule count from which they show in a virtual
// list.
const bigCustom = 50

func (p *Page) customSection(c *ui.Context, t *ui.Theme, runtime []model.Rule) {
	e := p.e
	all := e.API.CustomRules()
	hits := map[string]model.Rule{}
	for _, r := range runtime {
		if r.Custom {
			hits[r.Type+","+r.Payload] = r
		}
	}
	var list []model.CustomRule
	lq := p.lowerQuery()
	for _, r := range all {
		if matches(lq, r.Type, env.RuleTypeName(r.Type), r.Payload, r.Target, env.TargetName(r.Target)) {
			list = append(list, r)
		}
	}
	q := strings.TrimSpace(p.query)
	e.Kit.Section(c, i18n.T("自定义规则"), i18n.T("优先于订阅规则，修改后自动重新加载配置"), func() {
		if len(list) == 0 {
			msg := i18n.T("还没有自定义规则。可点击「添加规则」，或在连接页选中一条连接后添加。")
			if q != "" {
				msg = i18n.T("没有匹配的自定义规则")
			}
			ui.Text(c, msg).TextColor(t.TextMuted).Padding(10, 12)
			return
		}
		if len(list) > bigCustom {
			// Many rules: a virtual list builds only the rows in view.
			p.customList.Key = func(i int) any { return list[i].ID }
			ui.List(c.Key("custom"), &p.customList, len(list), func(i int) {
				p.customRow(c, t, list, i, all, hits, q)
			}).Height(5*40 + 20).FillWidth().Label(i18n.T("自定义规则"))
			return
		}
		ui.Scroll(c).MaxHeight(5*40 + 20).Children(func() {
			ui.Column(c).Dividers(1, t.Border).Children(func() { p.customRows(c, t, list, all, hits, q) })
		})
	})
}

// customRows draws the custom rules as rows.
func (p *Page) customRows(c *ui.Context, t *ui.Theme, list, all []model.CustomRule, hits map[string]model.Rule, q string) {
	for i := range list {
		p.customRow(c, t, list, i, all, hits, q)
	}
}

// customRow draws the custom rule list[i].
func (p *Page) customRow(c *ui.Context, t *ui.Theme, list []model.CustomRule, i int, all []model.CustomRule, hits map[string]model.Rule, q string) {
	e := p.e
	r := list[i]
	rule := r
	first := i == 0 && len(all) > 0 && all[0].ID == r.ID
	h, hasHit := hits[r.Type+","+r.Payload]
	ui.Row(c.Key(r.ID)).Gap(10).MinHeight(40).PaddingX(12).Children(func() {
		e.Kit.Tag(c, env.RuleTypeName(rule.Type), "gray")
		env.Highlight(c, rule.Payload, q, t.Text).Font("monospace").Grow(1).SingleLine()
		ui.Text(c, "→ "+env.TargetName(rule.Target)).TextColor(t.TextMuted)
		hitText := i18n.T("命中 0")
		if hasHit {
			hitText = i18n.Tf("命中 %s", env.FmtInt(h.HitCount))
			if h.HitCount > 0 {
				hitText += " · " + env.FmtAgo(h.HitAt)
			}
		}
		ui.Text(c, hitText).FontSize(11).TextColor(t.TextMuted).FontFeatures("tnum").Width(130).TextAlign(ui.End)
		ui.Button(c, "⋯").Label(i18n.T("规则操作")).Tooltip(i18n.T("更多")).PopoverMenuButton(func(m *ui.Menu) {
			if m.Item(i18n.T("编辑…")).Chosen() {
				p.form.Open(rule)
			}
			if m.Item(i18n.T("上移")).Disabled(first).Chosen() {
				e.Run(func() error { return e.API.MoveCustomRule(rule.ID, -1) }, "")
			}
			m.Separator()
			if m.Item(i18n.T("删除…")).Danger().Chosen() {
				e.Confirm(i18n.Tf("删除规则「%s」？", ruleform.Describe(rule)), i18n.T("删除后会重新生成配置并让内核重新加载。"), i18n.T("删除"), true, func() {
					e.Run(func() error { return e.API.DeleteCustomRule(rule.ID) }, i18n.T("规则已删除"))
				})
			}
		}, ui.PopoverMenuButtonOptions{At: ui.AnchorBottomEnd, Self: ui.AnchorTopEnd})
	})
}

// ---- subscription rules ----

// subCache is which subscription rules match the query, as positions in
// Rules() in the order shown, for the rules, query and order it was made
// for. The hit counts change every refresh without changing which rules
// there are, so the rows are taken fresh from Rules() at those positions;
// only the order by hits is sorted again each build.
type subCache struct {
	n     int    // len(Rules())
	sig   uint64 // of the rules' identities (rulesSig)
	query string // lowerQuery
	lang  string // TargetName translates 直连 / 拒绝
	sort  ui.SortOrder
	total int   // subscription rules
	pos   []int // matching positions, in order
}

var zero [1]byte

// rulesSig fingerprints which rules there are (not their hit counts).
func rulesSig(rules []model.Rule) uint64 {
	h := fnv.New64a()
	var b [9]byte
	for _, r := range rules {
		binary.LittleEndian.PutUint64(b[:8], uint64(r.Index))
		b[8] = 0
		if r.Custom {
			b[8] = 1
		}
		h.Write(b[:])
		for _, s := range [...]string{r.Type, r.Payload, r.Proxy} {
			io.WriteString(h, s)
			h.Write(zero[:])
		}
	}
	return h.Sum64()
}

// subRows returns the subscription rules shown and how many there are.
func (p *Page) subRows(rules []model.Rule) (rows []model.Rule, total int) {
	sc := &p.sub
	lq := p.lowerQuery()
	sig := rulesSig(rules)
	byHits := p.sort.Column == "hits"
	if sc.pos == nil || sc.n != len(rules) || sc.sig != sig || sc.query != lq || sc.lang != i18n.Lang() || (!byHits && sc.sort != p.sort) {
		*sc = subCache{n: len(rules), sig: sig, query: lq, lang: i18n.Lang(), sort: p.sort, pos: []int{}}
		var sub []model.Rule
		var at []int
		for i, r := range rules {
			if r.Custom {
				continue
			}
			sc.total++
			if matches(lq, r.Type, r.Payload, r.Proxy, env.TargetName(r.Proxy)) {
				sub = append(sub, r)
				at = append(at, i)
			}
		}
		// Sort positions with their rules.
		idx := make([]int, len(sub))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(i, j int) bool { return ruleLess(sub[idx[i]], sub[idx[j]], p.sort) })
		for _, i := range idx {
			sc.pos = append(sc.pos, at[i])
		}
	}
	rows = make([]model.Rule, len(sc.pos))
	for i, at := range sc.pos {
		rows[i] = rules[at]
	}
	if byHits {
		sortRules(rows, p.sort)
	}
	return rows, sc.total
}

func (p *Page) subSection(c *ui.Context, t *ui.Theme, rules []model.Rule) {
	e := p.e
	rows, total := p.subRows(rules)
	subName := ""
	for _, s := range e.API.Subscriptions() {
		if s.Current {
			subName = s.Name
		}
	}
	note := i18n.Tf("共 %d 条", total)
	if subName != "" {
		note = subName + " · " + note
	}
	q := strings.TrimSpace(p.query)
	ui.Column(c).Gap(8).Grow(1).Basis(0).MinHeight(160).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Baseline).Children(func() {
			ui.Text(c, i18n.T("订阅规则")).Bold()
			ui.Text(c, "· "+note).FontSize(11).TextColor(t.TextMuted)
		})
		if len(rows) == 0 {
			msg := i18n.T("订阅中没有规则")
			if q != "" {
				msg = i18n.Tf("没有匹配“%s”的规则", q)
			}
			ui.Text(c, msg).TextColor(t.TextMuted).Padding(16).TextAlign(ui.Center)
		} else {
			p.table.Key = func(i int) any { return rows[i].Index }
			p.table.Label = func(i int) string { return rows[i].Type + " " + rows[i].Payload }
			ui.Table(c.Key("table"), &p.table, env.I18nColumns(columns), len(rows), func(row, col int) {
				p.cell(c, t, rows[row], col, q)
			}).FillWidth().Grow(1).Radius(8).Border(1, t.Border).Label(i18n.T("订阅规则"))
		}
		ui.Text(c, i18n.T("命中次数自本次内核启动起统计。「停用」只在本次运行有效，重新加载配置后恢复；要长期修改，请使用自定义规则或覆写。")).
			FontSize(11).TextColor(t.TextMuted)
	})
}

func (p *Page) cell(c *ui.Context, t *ui.Theme, r model.Rule, col int, q string) {
	e := p.e
	muted := t.TextMuted
	fade := func(el ui.Element) ui.Element {
		if r.Disabled {
			el.Opacity(0.5)
		}
		return el
	}
	switch columns[col].ID {
	case "#":
		fade(ui.Text(c, fmt.Sprint(r.Index+1)).TextColor(muted).FontFeatures("tnum"))
	case "type":
		fade(env.Highlight(c, r.Type, q, muted).SingleLine())
	case "payload":
		ui.Row(c).Gap(6).Children(func() {
			if r.Payload == "" {
				fade(ui.Text(c, "—").TextColor(muted))
			} else {
				fade(env.Highlight(c, r.Payload, q, t.Text).Font("monospace").SingleLine())
			}
			if r.Size > 0 {
				fade(ui.Text(c, i18n.Tf("%s 条", env.FmtInt(int64(r.Size)))).FontSize(11).TextColor(muted).FontFeatures("tnum"))
			}
			if r.Disabled {
				e.Kit.Tag(c, i18n.T("已临时停用"), "orange")
			}
		})
	case "policy":
		fade(env.Highlight(c, env.TargetName(r.Proxy), q, t.Text).SingleLine())
	case "hits":
		if r.HitCount == 0 {
			fade(ui.Text(c, "0").TextColor(muted).FontFeatures("tnum"))
		} else {
			fade(ui.Text(c, env.FmtInt(r.HitCount)).FontFeatures("tnum"))
		}
	case "at":
		at := "—"
		if r.HitCount > 0 {
			at = env.FmtAgo(r.HitAt)
		}
		fade(ui.Text(c, at).TextColor(muted).SingleLine())
	case "action":
		label := i18n.T("停用")
		if r.Disabled {
			label = i18n.T("恢复")
		}
		idx, dis := r.Index, !r.Disabled
		ui.Button(c, label).FontSize(12).Padding(2, 8).Tooltip(i18n.T("仅本次运行有效，重新加载配置后恢复")).OnClick(func() {
			e.Run(func() error { return e.API.SetRuleDisabled(idx, dis) }, "")
		})
	}
}

func sortRules(rows []model.Rule, s ui.SortOrder) {
	sort.SliceStable(rows, func(i, j int) bool { return ruleLess(rows[i], rows[j], s) })
}

// ruleLess orders rules by s: by hits or by position.
func ruleLess(a, b model.Rule, s ui.SortOrder) bool {
	if s.Descending {
		a, b = b, a
	}
	if s.Column == "hits" {
		if a.HitCount != b.HitCount {
			return a.HitCount < b.HitCount
		}
	}
	return a.Index < b.Index
}

// ---- rule providers ----

func (p *Page) providers(c *ui.Context, t *ui.Theme) {
	e := p.e
	provs := e.API.RuleProviders()
	if len(provs) == 0 {
		return
	}
	e.Kit.Section(c, i18n.T("规则集"), i18n.T("订阅引用的 rule-providers，可单独更新"), func() {
		for _, pr := range provs {
			pr := pr
			desc := i18n.Tf("%s · %s 条 · 更新于 %s", behaviorName(pr.Behavior), env.FmtInt(int64(pr.Count)), env.FmtAgo(pr.UpdatedAt))
			e.Kit.Row(c, pr.Name, desc, func() {
				if pr.Updating {
					ui.Row(c).Gap(6).Children(func() {
						ui.Spinner(c)
						ui.Text(c, i18n.T("更新中…")).TextColor(t.TextMuted)
					})
					return
				}
				ui.Button(c, i18n.T("更新")).Disabled(pr.VehicleType != "HTTP").OnClick(func() {
					e.Run(func() error { return e.API.UpdateRuleProvider(pr.Name) }, i18n.Tf("规则集「%s」已更新", pr.Name))
				})
			})
		}
	})
}

func behaviorName(b string) string {
	switch b {
	case "domain":
		return i18n.T("域名")
	case "ipcidr":
		return i18n.T("IP 段")
	case "classical":
		return i18n.T("经典")
	}
	return b
}

// ---- 测试规则 (§6.8) ----

func (p *Page) testDialog(c *ui.Context, t *ui.Theme) {
	if !p.testOpen {
		return
	}
	p.mu.Lock()
	res, errText, testing := p.testResult, p.testErr, p.testing
	p.mu.Unlock()
	ui.Modal(c, &p.testOpen, func() {
		ui.Column(c.Key("test")).Width(440).Gap(12).Children(func() {
			ui.Text(c, i18n.T("测试规则")).FontSize(15).Bold()
			ui.Row(c).Gap(8).Children(func() {
				in := ui.TextInput(c.Key("host"), &p.testHost).Placeholder(i18n.T("域名或 IP，例如 www.google.com")).Font("monospace").Grow(1).AutoFocus().Label(i18n.T("域名或 IP"))
				if in.Submitted() {
					p.runTest()
				}
				label := i18n.T("测试")
				if testing {
					label = i18n.T("测试中…")
				}
				ui.PrimaryButton(c, label).Disabled(testing || strings.TrimSpace(p.testHost) == "").OnClick(p.runTest)
			})
			switch {
			case errText != "":
				ui.Text(c, errText).FontSize(12).TextColor(t.Danger)
			case res != nil:
				p.testResultView(c, t, *res)
			}
			ui.Text(c, i18n.T("测试会经内核实际访问一次该地址，再读取命中的规则。")).FontSize(11).TextColor(t.TextMuted)
			ui.Row(c).Justify(ui.End).Children(func() {
				if ui.Button(c, i18n.T("关闭")).Clicked() {
					p.testOpen = false
				}
			})
		})
	})
}

func (p *Page) testResultView(c *ui.Context, t *ui.Theme, r model.RuleMatch) {
	e := p.e
	ui.Column(c).Gap(6).Padding(12).Radius(8).Background(t.Surface).Border(1, t.Border).Selectable().Children(func() {
		if r.RuleIndex < 0 && r.Rule == "" {
			ui.Text(c, i18n.Tf("%s 未命中任何规则", r.Host)).Bold()
			ui.Text(c, i18n.T("内核没有报告命中的规则，可能是该地址无法访问。")).FontSize(12).TextColor(t.TextMuted)
			return
		}
		src := i18n.T("订阅")
		if r.Custom {
			src = i18n.T("自定义")
		}
		ui.Row(c).Gap(6).Children(func() {
			ui.Text(c, r.Host).Bold().Font("monospace")
			ui.Text(c, i18n.T("命中")).TextColor(t.TextMuted)
			e.Kit.Tag(c, src, "gray")
			if r.RuleIndex >= 0 {
				ui.Text(c, i18n.Tf("规则 #%d", r.RuleIndex+1)).FontFeatures("tnum")
			}
		})
		ui.Text(c, r.Rule).Font("monospace").FontSize(12)
		chain := []string{env.TargetName(r.Policy)}
		for i, hop := range r.Chain {
			if i == 0 && hop == r.Policy {
				continue
			}
			chain = append(chain, env.TargetName(hop))
		}
		ui.Text(c, i18n.Tf("走向 %s", strings.Join(chain, " › "))).FontSize(12)
	})
}

func (p *Page) runTest() {
	e := p.e
	host := strings.TrimSpace(p.testHost)
	if host == "" || p.testing {
		return
	}
	p.mu.Lock()
	p.testing, p.testErr, p.testResult = true, "", nil
	p.mu.Unlock()
	e.Run(func() error {
		res, err := e.API.TestRule(host)
		p.mu.Lock()
		defer p.mu.Unlock()
		p.testing = false
		if err != nil {
			p.testErr = err.Error()
		} else {
			p.testResult = &res
		}
		return nil
	}, "")
}

// ---- helpers ----

// Topics are the service topics this page's view reads; the shell
// rebuilds it only for these (and its own).
func (p *Page) Topics() []app.Topic {
	return []app.Topic{app.TopicStatus, app.TopicRules, app.TopicProxies, app.TopicSubs}
}
