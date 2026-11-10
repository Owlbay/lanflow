package settings

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// dnsForm is the DNS tab's editable copy of model.DNSSettings, with the
// lists as multi-line texts.
type dnsForm struct {
	takeOver         bool
	mode             int // 0 Fake-IP, 1 真实 IP
	ns, pns, direct  string
	policy           []model.KV
	filter           string
	respect, sysHost bool
	hosts            []model.KV
}

// recommendedDNS is what 使用推荐设置 fills in.
var recommendedDNS = model.DNSSettings{
	TakeOver:              true,
	EnhancedMode:          "fake-ip",
	Nameserver:            []string{"https://dns.alidns.com/dns-query", "https://doh.pub/dns-query"},
	ProxyServerNameserver: []string{"https://dns.alidns.com/dns-query"},
	DirectNameserver:      []string{"system"},
	Policy:                []model.KV{{Key: "geosite:cn", Value: "https://dns.alidns.com/dns-query"}, {Key: "geosite:geolocation-!cn", Value: "https://dns.cloudflare.com/dns-query"}},
	FakeIPFilter:          []string{"*.lan", "*.local", "localhost.ptlogin2.qq.com", "time.*.com", "+.msftconnecttest.com", "+.msftncsi.com"},
	RespectRules:          true,
	UseSystemHosts:        true,
}

func dnsFormFrom(d model.DNSSettings) dnsForm {
	f := dnsForm{takeOver: d.TakeOver, ns: joinLines(d.Nameserver), pns: joinLines(d.ProxyServerNameserver), direct: joinLines(d.DirectNameserver),
		filter: joinLines(d.FakeIPFilter), respect: d.RespectRules, sysHost: d.UseSystemHosts}
	if d.EnhancedMode == "redir-host" {
		f.mode = 1
	}
	f.policy = append([]model.KV(nil), d.Policy...)
	f.hosts = append([]model.KV(nil), d.Hosts...)
	return f
}

func (f dnsForm) toSettings() model.DNSSettings {
	d := model.DNSSettings{TakeOver: f.takeOver, EnhancedMode: "fake-ip", Nameserver: lines(f.ns), ProxyServerNameserver: lines(f.pns),
		DirectNameserver: lines(f.direct), FakeIPFilter: lines(f.filter), RespectRules: f.respect, UseSystemHosts: f.sysHost}
	if f.mode == 1 {
		d.EnhancedMode = "redir-host"
	}
	d.Policy = cleanKV(f.policy)
	d.Hosts = cleanKV(f.hosts)
	return d
}

// cleanKV trims pairs and drops those without a key (nil when none).
func cleanKV(kvs []model.KV) []model.KV {
	var out []model.KV
	for _, kv := range kvs {
		k, v := strings.TrimSpace(kv.Key), strings.TrimSpace(kv.Value)
		if k != "" {
			out = append(out, model.KV{Key: k, Value: v})
		}
	}
	return out
}

// canonDNS normalises a settings value so that form round trips compare equal.
func canonDNS(d model.DNSSettings) model.DNSSettings {
	return dnsFormFrom(d).toSettings()
}

func dnsEqual(a, b model.DNSSettings) bool { return reflect.DeepEqual(canonDNS(a), canonDNS(b)) }

func (p *Page) dnsDirty() bool {
	return p.dnsInit && !reflect.DeepEqual(p.dns.toSettings(), canonDNS(p.dnsLoaded))
}

func (p *Page) discardDNS() {
	p.dns = dnsFormFrom(p.dnsLoaded)
	p.dnsSession++
}

func (p *Page) applyDNS() {
	e := p.e
	if p.applying {
		return
	}
	d := p.dns.toSettings()
	p.applying = true
	e.Run(func() error {
		err := e.API.ApplyDNS(d)
		p.mu.Lock()
		p.applying = false
		if err == nil {
			p.dnsLoaded = d
			p.toast = i18n.T("DNS 设置已应用")
		}
		p.mu.Unlock()
		return err
	}, "")
}

func (p *Page) dnsTab(c *ui.Context, t *ui.Theme, s model.Settings) {
	e := p.e
	f := &p.dns
	take := f.takeOver
	ro := !take
	subName := currentSubName(e)
	// In 跟随订阅 mode the fields show the current subscription's dns
	// section (after overrides), not Lanflow's stored draft.
	view := f
	var eff model.EffectiveDNS
	if ro {
		eff = e.API.EffectiveDNS()
		shown := dnsFormFrom(eff.Settings)
		view = &shown
	}
	// Two columns at ≥1100 pt: the source and the resolvers on the left,
	// the per-domain servers, Fake-IP and hosts on the right.
	ui.Column(c.Key(p.dnsSession)).Gap(20).Children(func() {
		env.Columns(c, 20, func() {
			e.Kit.Section(c, "DNS", "", func() {
				e.Kit.Row(c, i18n.T("DNS 设置来源"), "", func() {
					src := 0
					if take {
						src = 1
					}
					if ui.Segmented(c.Key("dns-src"), &src, i18n.T("跟随订阅"), i18n.T("由澜流接管")).Label(i18n.T("DNS 设置来源")).Changed() {
						f.takeOver = src == 1
					}
				})
				if !take {
					// docs/design.md:536: the banner names the subscription whose
					// dns section the read-only fields show.
					msg := i18n.Tf("正在使用订阅「%s」中的 DNS 设置，以下内容只读。如需修改，请切换到「由澜流接管」。", subName)
					switch {
					case eff.Source == model.EffectiveDefault:
						msg = i18n.Tf("订阅「%s」中没有 DNS 设置，内核使用 Mihomo 的默认值：不启用内置 DNS 服务器，由系统解析器解析域名。以下内容只读。如需修改，请切换到「由澜流接管」。", subName)
					case !eff.Enabled:
						msg += i18n.T("该订阅没有启用内置 DNS（dns.enable 为 false），内核直接用系统解析器解析域名。")
					}
					e.Kit.Banner(c, "info", msg, nil)
				} else {
					e.Kit.Row(c, i18n.T("接管后，以下设置整体替换订阅中的 dns 段"), "", func() {
						ui.Button(c, i18n.T("使用推荐设置")).OnClick(func() {
							*f = dnsFormFrom(recommendedDNS)
							p.dnsSession++
						})
					})
				}
			})
			e.Kit.Section(c, i18n.T("解析"), "", func() {
				e.Kit.Row(c, i18n.T("模式"), i18n.T("Fake-IP 响应更快；真实 IP 兼容性更好"), func() {
					ui.Segmented(c.Key("dns-mode"), &view.mode, "Fake-IP", i18n.T("真实 IP")).Label(i18n.T("模式")).Disabled(ro)
				})
				p.textRow(c, t, i18n.T("上游服务器"), i18n.T("每行一个，支持 DoH（https://）、DoT（tls://）和普通地址"), "dns-ns", &view.ns, 2, ro)
				p.textRow(c, t, i18n.T("节点域名解析"), i18n.T("只用于解析节点服务器地址（proxy-server-nameserver）"), "dns-pns", &view.pns, 1, ro)
				p.textRow(c, t, i18n.T("直连域名解析"), i18n.T("走直连的域名使用的服务器；system 表示系统 DNS（direct-nameserver）"), "dns-direct", &view.direct, 1, ro)
				e.Kit.Row(c, i18n.T("遵循分流规则"), i18n.T("DNS 请求本身也按规则选择直连或代理（respect-rules）"), func() {
					ui.Switch(c.Key("dns-respect"), &view.respect).Label(i18n.T("遵循分流规则")).Disabled(ro)
				})
			})
		}, func() {
			e.Kit.Section(c, i18n.T("按域名指定服务器"), i18n.T("域名或规则集 → 服务器（nameserver-policy）"), func() {
				p.kvRows(c, t, "dns-policy", &view.policy, i18n.T("geosite:cn 或 +.example.com"), "https://dns.alidns.com/dns-query", ro)
			})
			e.Kit.Section(c, i18n.T("Fake-IP 与 Hosts"), "", func() {
				p.textRow(c, t, i18n.T("Fake-IP 排除"), i18n.T("这些域名始终返回真实 IP，每行一个，+. 表示包含所有子域名"), "dns-filter", &view.filter, 3, ro)
				e.Kit.Row(c, i18n.T("使用系统 hosts"), "", func() {
					ui.Switch(c.Key("dns-hosts"), &view.sysHost).Label(i18n.T("使用系统 hosts")).Disabled(ro)
				})
				desc := i18n.T("未设置")
				if n := len(cleanKV(view.hosts)); n > 0 {
					desc = i18n.Tf("%d 条", n)
				}
				e.Kit.Row(c, i18n.T("自定义 hosts"), desc, func() {
					ui.Button(c, i18n.T("编辑…")).Disabled(ro).OnClick(func() {
						var b strings.Builder
						for _, kv := range f.hosts {
							b.WriteString(kv.Key + " " + kv.Value + "\n")
						}
						p.hosts.text = b.String()
						p.hosts.session++
						p.hosts.open = true
					})
				})
			})
		})
	})
	_ = s
}

// textRow is a multi-line list field.
func (p *Page) textRow(c *ui.Context, t *ui.Theme, label, desc, key string, text *string, minLines int, ro bool) {
	ui.Column(c).Gap(6).Padding(8, 12).Children(func() {
		ui.Text(c, label)
		if desc != "" {
			ui.Text(c, desc).FontSize(11).TextColor(t.TextMuted)
		}
		ui.TextArea(c.Key(key), text).Font("monospace").FontSize(12).Lines(minLines, 8).ReadOnly(ro).Label(label)
	})
}

// kvRows edits a list of key → value pairs.
func (p *Page) kvRows(c *ui.Context, t *ui.Theme, key string, kvs *[]model.KV, keyHint, valueHint string, ro bool) {
	var remove = -1
	for i := range *kvs {
		i := i
		ui.Row(c.Key(key + "-" + itoa(i))).Gap(8).PaddingX(12).MinHeight(36).Children(func() {
			ui.TextInput(c.Key("k"), &(*kvs)[i].Key).Placeholder(keyHint).Font("monospace").FontSize(12).Grow(1).Basis(0).ReadOnly(ro).Label(i18n.T("域名或规则集"))
			ui.Text(c, "→").TextColor(t.TextMuted)
			ui.TextInput(c.Key("v"), &(*kvs)[i].Value).Placeholder(valueHint).Font("monospace").FontSize(12).Grow(1).Basis(0).ReadOnly(ro).Label(i18n.T("服务器"))
			ui.Button(c, "").Label(i18n.T("删除")).Tooltip(i18n.T("删除")).Padding(0, 4).Disabled(ro).Children(func() {
				ui.Text(c, "✕").FontSize(11)
			}).OnClick(func() { remove = i })
		})
	}
	ui.Row(c).PaddingX(12).MinHeight(36).Children(func() {
		ui.Button(c, "").Disabled(ro).Children(func() {
			p.e.Kit.Icon(c, "plus", 12)
			ui.Text(c, i18n.T("添加"))
		}).OnClick(func() { *kvs = append(*kvs, model.KV{}) })
	})
	if remove >= 0 && remove < len(*kvs) {
		*kvs = append((*kvs)[:remove], (*kvs)[remove+1:]...)
	}
}

func (p *Page) hostsDialog(c *ui.Context, t *ui.Theme) {
	if !p.hosts.open {
		return
	}
	ui.Modal(c, &p.hosts.open, func() {
		ui.Column(c.Key(p.hosts.session)).Width(460).Gap(12).Children(func() {
			ui.Text(c, i18n.T("自定义 hosts")).FontSize(15).Bold()
			ui.Text(c, i18n.T("每行一条：域名，空格，IP 或另一个域名。例如 router.lan 192.168.1.1")).FontSize(12).TextColor(t.TextMuted)
			ui.TextArea(c.Key("hosts"), &p.hosts.text).Font("monospace").FontSize(12).Height(200).Label(i18n.T("自定义 hosts"))
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, i18n.T("取消")).Clicked() {
					p.hosts.open = false
				}
				ui.PrimaryButton(c, i18n.T("完成")).OnClick(func() {
					var kvs []model.KV
					for _, l := range lines(p.hosts.text) {
						fs := strings.Fields(l)
						kv := model.KV{Key: fs[0]}
						if len(fs) > 1 {
							kv.Value = fs[1]
						}
						kvs = append(kvs, kv)
					}
					p.dns.hosts = kvs
					p.hosts.open = false
				})
			})
		})
	})
}

// currentSubName returns the name of the subscription in use, or "当前订阅".
func currentSubName(e *env.Env) string {
	for _, s := range e.API.Subscriptions() {
		if s.Current {
			return s.Name
		}
	}
	return i18n.T("当前订阅")
}

func itoa(n int) string { return strconv.Itoa(n) }
