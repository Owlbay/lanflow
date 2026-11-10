package settings

import (
	"reflect"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// snfForm is the 嗅探 tab's editable copy of model.SnifferSettings, minus
// Enable, which applies at once through SaveSettings.
type snfForm struct {
	takeOver                       bool
	http, tls, quic                bool
	httpPorts, tlsPorts, quicPorts string
	override, pureIP               bool
	force, skip, skipAddr          string
}

func snfFormFrom(s model.SnifferSettings) snfForm {
	return snfForm{takeOver: s.TakeOver, http: s.HTTP, tls: s.TLS, quic: s.QUIC, httpPorts: s.HTTPPorts, tlsPorts: s.TLSPorts, quicPorts: s.QUICPorts,
		override: s.OverrideDestination, pureIP: s.ParsePureIP, force: joinLines(s.ForceDomain), skip: joinLines(s.SkipDomain), skipAddr: joinLines(s.SkipDstAddress)}
}

// toSettings returns the sniffer settings with enable taken from the saved value.
func (f snfForm) toSettings(enable bool) model.SnifferSettings {
	return model.SnifferSettings{Enable: enable, TakeOver: f.takeOver, HTTP: f.http, TLS: f.tls, QUIC: f.quic,
		HTTPPorts: ports(f.httpPorts), TLSPorts: ports(f.tlsPorts), QUICPorts: ports(f.quicPorts),
		OverrideDestination: f.override, ParsePureIP: f.pureIP, ForceDomain: lines(f.force), SkipDomain: lines(f.skip), SkipDstAddress: lines(f.skipAddr)}
}

// ports normalises "80,8080-8880" to "80, 8080-8880".
func ports(s string) string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ", ")
}

func canonSnf(s model.SnifferSettings) model.SnifferSettings {
	return snfFormFrom(s).toSettings(s.Enable)
}

func snfEqual(a, b model.SnifferSettings) bool { return reflect.DeepEqual(canonSnf(a), canonSnf(b)) }

func (p *Page) snfDirty() bool {
	return p.snfInit && !reflect.DeepEqual(p.snf.toSettings(p.snfLoaded.Enable), canonSnf(p.snfLoaded))
}

func (p *Page) discardSniffer() { p.snf = snfFormFrom(p.snfLoaded) }

func (p *Page) applySniffer() {
	e := p.e
	if p.applying {
		return
	}
	s := p.snf.toSettings(p.current().Sniffer.Enable)
	p.applying = true
	e.Run(func() error {
		err := e.API.ApplySniffer(s)
		p.mu.Lock()
		p.applying = false
		if err == nil {
			p.snfLoaded = s
			p.toast = i18n.T("嗅探设置已应用")
		}
		p.mu.Unlock()
		return err
	}, "")
}

func (p *Page) snifferTab(c *ui.Context, t *ui.Theme, s model.Settings) {
	e := p.e
	f := &p.snf
	enabled := s.Sniffer.Enable
	take := f.takeOver
	ro := !take || !enabled
	// In 跟随订阅 mode the fields show the current subscription's sniffer
	// section (after overrides), not Lanflow's stored draft.
	view := f
	var eff model.EffectiveSniffer
	if !take {
		eff = e.API.EffectiveSniffer()
		shown := snfFormFrom(eff.Settings)
		view = &shown
	}
	// Two columns at ≥1100 pt: the switch, source and protocols on the
	// left, the behaviour on the right.
	env.Columns(c, 20, func() {
		e.Kit.Section(c, i18n.T("嗅探"), "", func() {
			p.switchRow(c, i18n.T("启用嗅探"), i18n.T("从 HTTP、TLS、QUIC 流量中读出真实域名，让按域名的规则对直接用 IP 连接的应用也生效。立即生效"), enabled, func(on bool) {
				s.Sniffer.Enable = on
				// Keep the saved value in step with the form's base so the
				// switch alone never counts as an unapplied change.
				p.snfLoaded.Enable = on
				p.save(s, "", nil)
			})
			e.Kit.Row(c, i18n.T("嗅探设置来源"), "", func() {
				src := 0
				if take {
					src = 1
				}
				if ui.Segmented(c.Key("snf-src"), &src, i18n.T("跟随订阅"), i18n.T("由澜流接管")).Label(i18n.T("嗅探设置来源")).Disabled(!enabled).Changed() {
					f.takeOver = src == 1
				}
			})
			if enabled && !take {
				msg := i18n.Tf("正在使用订阅「%s」中的嗅探设置，以下内容只读。如需修改，请切换到「由澜流接管」。", currentSubName(e))
				if eff.Source == model.EffectiveDefault {
					msg = i18n.Tf("订阅「%s」中没有嗅探设置，内核使用 Mihomo 的默认值（不嗅探任何协议）。以下内容只读。如需修改，请切换到「由澜流接管」。", currentSubName(e))
				}
				e.Kit.Banner(c, "info", msg, nil)
			}
		})
		e.Kit.Section(c, i18n.T("协议与端口"), "", func() {
			proto := func(name, key string, portsText *string, on *bool) {
				e.Kit.Row(c, name, "", func() {
					ui.Row(c).Gap(10).Children(func() {
						ui.TextInput(c.Key(key+"-ports"), portsText).Font("monospace").FontSize(12).Width(160).ReadOnly(ro).Label(i18n.Tf("%s 端口", name)).Placeholder("443")
						ui.Switch(c.Key(key+"-on"), on).Label(name).Disabled(ro)
					})
				})
			}
			proto("HTTP", "snf-http", &view.httpPorts, &view.http)
			proto("TLS", "snf-tls", &view.tlsPorts, &view.tls)
			proto("QUIC", "snf-quic", &view.quicPorts, &view.quic)
		})
	}, func() {
		e.Kit.Section(c, i18n.T("行为"), "", func() {
			e.Kit.Row(c, i18n.T("用嗅探结果覆盖目标地址"), i18n.T("按嗅探到的域名重新解析并连接（override-destination）"), func() {
				ui.Switch(c.Key("snf-override"), &view.override).Label(i18n.T("用嗅探结果覆盖目标地址")).Disabled(ro)
			})
			e.Kit.Row(c, i18n.T("嗅探纯 IP 连接"), i18n.T("对没有域名的连接也尝试嗅探（parse-pure-ip）"), func() {
				ui.Switch(c.Key("snf-pure"), &view.pureIP).Label(i18n.T("嗅探纯 IP 连接")).Disabled(ro)
			})
			p.textRow(c, t, i18n.T("强制嗅探的域名"), i18n.T("每行一个，+. 表示包含所有子域名（force-domain）"), "snf-force", &view.force, 1, ro)
			p.textRow(c, t, i18n.T("跳过嗅探的域名"), i18n.T("每行一个（skip-domain）"), "snf-skip", &view.skip, 1, ro)
			p.textRow(c, t, i18n.T("跳过嗅探的目标地址"), i18n.T("IP 或网段，每行一个（skip-dst-address）"), "snf-skipaddr", &view.skipAddr, 1, ro)
		})
	})
}
