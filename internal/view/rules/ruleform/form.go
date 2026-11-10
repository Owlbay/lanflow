// Package ruleform is the add / edit custom-rule dialog (docs/design.md
// §6.7), shared by the rules page and the connections page ("此域名始终走…").
package ruleform

import (
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// Types are the five custom rule types the dialog offers, in menu order.
var Types = []string{"DOMAIN-SUFFIX", "DOMAIN", "DOMAIN-KEYWORD", "IP-CIDR", "PROCESS-NAME"}

var placeholders = map[string]string{
	"DOMAIN-SUFFIX":  "example.com",
	"DOMAIN":         "www.example.com",
	"DOMAIN-KEYWORD": "google",
	"IP-CIDR":        "192.168.0.0/16",
	"PROCESS-NAME":   "Steam",
}

// Form is the dialog's state; keep one per page.
type Form struct {
	open    bool
	rule    model.CustomRule
	typ     string // Chinese type name bound to the select
	target  string // Chinese target name bound to the select
	err     string
	saving  bool
	saved   bool // set by the save job; View turns it into a toast
	session int
}

// Open shows the dialog for r: an empty ID adds a new rule, otherwise it
// edits r.
func (f *Form) Open(r model.CustomRule) {
	if r.Type == "" {
		r.Type = Types[0]
	}
	f.rule = r
	f.typ = env.RuleTypeName(r.Type)
	f.target = env.TargetName(r.Target)
	f.err, f.saving, f.open = "", false, true
	f.session++
}

// IsOpen reports whether the dialog shows.
func (f *Form) IsOpen() bool { return f.open }

// Rule returns the rule as currently edited (for tests).
func (f *Form) Rule() model.CustomRule { return f.rule }

// Error returns the validation or save error shown in the dialog.
func (f *Form) Error() string { return f.err }

// View draws the dialog while it is open. Call it once per build.
func (f *Form) View(c *ui.Context, e *env.Env) {
	if f.saved {
		f.saved = false
		e.Toast(i18n.T("规则已保存"), false)
	}
	if !f.open {
		return
	}
	t := c.Theme()
	typeNames := make([]string, len(Types))
	for i, ty := range Types {
		typeNames[i] = env.RuleTypeName(ty)
	}
	targets, targetNames := targetOptions(e)
	f.typ = env.RuleTypeName(f.rule.Type)
	if i := indexOf(targets, f.rule.Target); i >= 0 {
		f.target = targetNames[i]
	}
	if f.target == "" || !contains(targetNames, f.target) {
		f.target = targetNames[0]
	}
	if f.rule.Target == "" {
		f.rule.Target = targets[0]
	}
	title := i18n.T("添加规则")
	if f.rule.ID != "" {
		title = i18n.T("编辑规则")
	}
	ui.Modal(c, &f.open, func() {
		ui.Column(c.Key(f.session)).Width(420).Gap(12).Children(func() {
			ui.Text(c, title).FontSize(15).Bold()
			ui.Form(c, func() {
				ui.Field(c, i18n.T("类型"), func() {
					if ui.Select(c, &f.typ, typeNames).Changed() {
						for i, n := range typeNames {
							if n == f.typ {
								f.rule.Type = Types[i]
							}
						}
						f.err = ""
					}
				})
				ui.Field(c, i18n.T("内容"), func() {
					in := ui.TextInput(c.Key("payload"), &f.rule.Payload).Placeholder(placeholders[f.rule.Type]).
						Font("monospace").Width(260).Invalid(f.err != "")
					if f.rule.ID == "" {
						in.AutoFocus()
					}
					if in.Submitted() {
						f.save(e)
					}
					if in.Changed() {
						f.err = ""
					}
				})
				ui.Field(c, i18n.T("走向"), func() {
					if ui.Select(c, &f.target, targetNames).Changed() {
						for i, n := range targetNames {
							if n == f.target {
								f.rule.Target = targets[i]
							}
						}
					}
				})
			})
			ui.Text(c, i18n.T("自定义规则排在订阅规则之前，保存后会重新生成配置并让内核重新加载。")).FontSize(11).TextColor(t.TextMuted)
			if f.err != "" {
				ui.Text(c, f.err).FontSize(12).TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).Children(func() {
				if f.rule.ID != "" {
					id := f.rule.ID
					ui.Button(c, i18n.T("删除")).TextColor(t.Danger).Disabled(f.saving).OnClick(func() {
						e.Confirm(i18n.T("删除这条规则？"), i18n.T("删除后会重新生成配置并让内核重新加载。"), i18n.T("删除"), true, func() {
							f.open = false
							e.Run(func() error { return e.API.DeleteCustomRule(id) }, i18n.T("规则已删除"))
						})
					})
				}
				ui.Spacer(c)
				if ui.Button(c, i18n.T("取消")).Disabled(f.saving).Clicked() {
					f.open = false
				}
				label := i18n.T("保存")
				if f.saving {
					label = i18n.T("保存中…")
				}
				ui.PrimaryButton(c, label).Disabled(f.saving).OnClick(func() { f.save(e) })
			})
		})
	})
}

func (f *Form) save(e *env.Env) {
	if f.saving {
		return
	}
	r := f.rule
	r.Payload = strings.TrimSpace(r.Payload)
	if msg := Validate(r); msg != "" {
		f.err = msg
		return
	}
	f.err, f.saving = "", true
	e.Run(func() error {
		_, err := e.API.SaveCustomRule(r)
		if err != nil {
			f.saving, f.err = false, err.Error()
			return nil // shown inline; the dialog stays open
		}
		f.saving, f.open, f.saved = false, false, true
		return nil
	}, "")
}

var domainRe = regexp.MustCompile(`^[A-Za-z0-9*+]([A-Za-z0-9*+_-]*\.)*[A-Za-z0-9*+_-]+$`)

// Validate checks a rule and returns the user-facing problem, or "".
func Validate(r model.CustomRule) string {
	p := strings.TrimSpace(r.Payload)
	if p == "" {
		return i18n.T("请填写规则内容")
	}
	if r.Target == "" {
		return i18n.T("请选择走向")
	}
	switch r.Type {
	case "IP-CIDR":
		if strings.Contains(p, "/") {
			if _, err := netip.ParsePrefix(p); err != nil {
				return i18n.T("IP 段格式不正确，例如 192.168.0.0/16 或 2001:db8::/32")
			}
		} else if net.ParseIP(p) == nil {
			return i18n.T("IP 段格式不正确，例如 192.168.0.0/16（单个 IP 可写成 1.2.3.4/32）")
		}
	case "DOMAIN", "DOMAIN-SUFFIX":
		if strings.ContainsAny(p, " \t/:") || !domainRe.MatchString(p) {
			return i18n.T("域名格式不正确，例如 example.com（不含协议、端口和路径）")
		}
	case "DOMAIN-KEYWORD":
		if strings.ContainsAny(p, " \t,") {
			return i18n.T("关键字不能包含空格或逗号")
		}
	case "PROCESS-NAME":
		if strings.ContainsAny(p, ",") {
			return i18n.T("进程名不能包含逗号")
		}
	default:
		return i18n.T("不支持的规则类型")
	}
	return ""
}

// SuffixOf returns a DOMAIN-SUFFIX payload for a connection host such as
// "rr3---sn-i3b7kn7s.googlevideo.com:443": the registrable-ish suffix
// ("googlevideo.com"), honouring common second-level public suffixes such
// as "co.uk" and "com.cn". An IP address is returned as is.
func SuffixOf(host string) string {
	h := host
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i+1:], "]") && strings.Count(h, ":") == 1 {
		h = h[:i]
	}
	h = strings.Trim(h, "[]")
	if net.ParseIP(h) != nil {
		return h
	}
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	parts := strings.Split(h, ".")
	if len(parts) <= 2 {
		return h
	}
	n := 2
	sld := parts[len(parts)-2] + "." + parts[len(parts)-1]
	if len(parts) >= 3 && secondLevel[sld] {
		n = 3
	}
	return strings.Join(parts[len(parts)-n:], ".")
}

// CIDROf returns an IP-CIDR payload for an IP ("1.2.3.4" → "1.2.3.4/32").
func CIDROf(ip string) string {
	a, err := netip.ParseAddr(strings.Trim(ip, "[]"))
	if err != nil {
		return ip
	}
	if a.Is4() {
		return a.String() + "/32"
	}
	return a.String() + "/128"
}

var secondLevel = map[string]bool{
	"co.uk": true, "org.uk": true, "ac.uk": true, "gov.uk": true, "me.uk": true,
	"com.cn": true, "net.cn": true, "org.cn": true, "gov.cn": true, "edu.cn": true,
	"com.hk": true, "org.hk": true, "com.tw": true, "org.tw": true, "com.au": true,
	"co.jp": true, "ne.jp": true, "or.jp": true, "co.kr": true, "com.sg": true,
	"com.br": true, "com.mx": true, "co.nz": true, "co.in": true, "com.tr": true,
}

// targetOptions lists the selectable targets (policy groups, 直连, 拒绝)
// and their display names.
func targetOptions(e *env.Env) (values, names []string) {
	px := e.API.Proxies()
	for _, g := range px.Groups {
		if g.Hidden || g.Name == "GLOBAL" {
			continue
		}
		values = append(values, g.Name)
		names = append(names, g.Name)
	}
	values = append(values, "DIRECT", "REJECT")
	names = append(names, env.TargetName("DIRECT"), env.TargetName("REJECT"))
	return values, names
}

func indexOf(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Describe returns "域名后缀 openai.com → 节点选择" for toasts and lists.
func Describe(r model.CustomRule) string {
	return fmt.Sprintf("%s %s → %s", env.RuleTypeName(r.Type), r.Payload, env.TargetName(r.Target))
}
