package settings

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// aTrust 兼容 › 配置…: the user's lists (model.ATrustConfig) edited one item
// per line, imported from a file (Lanflow's YAML/JSON or a JavaScript
// override script) for review, exported, with the reference template to copy.

// atrustDialog is the sheet's state.
type atrustDialog struct {
	open    bool
	session int
	form    atrustForm
	pending *model.ATrustConfig // an import waiting to fill the form (set by a job, guarded by Page.mu)
}

// atrustForm is the sheet's fields, one entry per line.
type atrustForm struct {
	corp, direct, dns, exclude, directCIDR string
	always                                 bool
}

func atrustFormFrom(cfg model.ATrustConfig) atrustForm {
	return atrustForm{
		corp: joinLines(cfg.CorpDomains), direct: joinLines(cfg.DirectDomains), dns: joinLines(cfg.CorpDNS),
		exclude: joinLines(cfg.ExcludeCIDRs), directCIDR: joinLines(cfg.DirectCIDRs), always: cfg.Always,
	}
}

// Field keys of the validation messages.
const (
	fieldCorp       = "corp"
	fieldDirect     = "direct"
	fieldDNS        = "dns"
	fieldExclude    = "exclude"
	fieldDirectCIDR = "directCIDR"
)

// parse validates every line and returns the configuration and one
// message per field in error (the first problem of that field).
func (f atrustForm) parse() (model.ATrustConfig, map[string]string) {
	cfg := model.ATrustConfig{Always: f.always}
	errs := map[string]string{}
	fill := func(field, text string, dst *[]string, norm func(string) (string, bool), bad string) {
		for i, l := range strings.Split(text, "\n") {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			v, ok := norm(l)
			if !ok {
				if errs[field] == "" {
					errs[field] = i18n.Tf(bad, i+1, l)
				}
				continue
			}
			found := false
			for _, x := range *dst {
				if x == v {
					found = true
				}
			}
			if !found {
				*dst = append(*dst, v)
			}
		}
	}
	fill(fieldCorp, f.corp, &cfg.CorpDomains, app.NormDomain, "第 %d 行不是有效的域名：%s")
	fill(fieldDirect, f.direct, &cfg.DirectDomains, app.NormDomain, "第 %d 行不是有效的域名：%s")
	fill(fieldDNS, f.dns, &cfg.CorpDNS, app.NormIP, "第 %d 行不是有效的 IP 地址：%s")
	fill(fieldExclude, f.exclude, &cfg.ExcludeCIDRs, app.NormCIDR, "第 %d 行不是有效的网段：%s")
	fill(fieldDirectCIDR, f.directCIDR, &cfg.DirectCIDRs, app.NormCIDR, "第 %d 行不是有效的网段：%s")
	for _, d := range cfg.DirectDomains {
		if errs[fieldDirect] == "" && containsStr(cfg.CorpDomains, d) {
			errs[fieldDirect] = i18n.Tf("%s 已在「内网域名」中", d)
		}
	}
	for _, c := range cfg.DirectCIDRs {
		if errs[fieldDirectCIDR] == "" && containsStr(cfg.ExcludeCIDRs, c) {
			errs[fieldDirectCIDR] = i18n.Tf("%s 已在「不进入 TUN 的网段」中", c)
		}
	}
	// the kit's pitfall: a range holding the company DNS must stay in the
	// TUN, or the DNS hijack misses the queries sent to it
	for _, c := range cfg.ExcludeCIDRs {
		for _, d := range cfg.CorpDNS {
			if errs[fieldExclude] == "" && cidrHolds(c, d) {
				errs[fieldExclude] = i18n.Tf("%s 包含公司 DNS %s，排除后 DNS 劫持会失效；请改放到「仅直连的网段」", c, d)
			}
		}
	}
	return cfg, errs
}

func cidrHolds(cidr, ip string) bool {
	p, err1 := netip.ParsePrefix(cidr)
	a, err2 := netip.ParseAddr(ip)
	return err1 == nil && err2 == nil && p.Contains(a)
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// atrustRowDesc words the setting's row: what detection found and what is
// configured.
func atrustRowDesc(a model.ATrustState, cfg model.ATrustConfig) string {
	var parts []string
	if a.Running {
		parts = append(parts, i18n.Tf("已检测到 aTrust：%d 个内网网段、%d 个内网域名直接连接", len(a.CIDRs), len(a.Domains)))
		if len(a.Gateways) > 0 {
			parts = append(parts, i18n.Tf("网关 %s", strings.Join(a.Gateways, ", ")))
		}
	} else {
		parts = append(parts, i18n.T("检测到深信服 aTrust 时，内网网段和域名直接连接、不进入 TUN，系统代理也绕过它们"))
	}
	if !cfg.Empty() {
		s := i18n.Tf("已配置 %d 个域名、%d 个网段", len(cfg.CorpDomains)+len(cfg.DirectDomains), len(cfg.ExcludeCIDRs)+len(cfg.DirectCIDRs))
		if cfg.Always {
			s += i18n.T("（未运行 aTrust 时也应用）")
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " · ")
}

// atrustRow is the 「aTrust 兼容」 setting: the switch and the 配置… button.
func (p *Page) atrustRow(c *ui.Context, s model.Settings, st model.Status) {
	label := i18n.T("aTrust 兼容")
	p.e.Kit.Row(c, label, atrustRowDesc(st.ATrust, s.ATrust), func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, i18n.T("配置…")).Clicked() {
				p.atrust.form = atrustFormFrom(s.ATrust)
				p.atrust.session++
				p.atrust.open = true
			}
			v := s.ATrustCompat
			if ui.Switch(c.Key(label), &v).Label(label).Disabled(p.saving).Changed() {
				s.ATrustCompat = v
				p.save(s, "", nil)
			}
		})
	})
}

func (p *Page) atrustDialog(c *ui.Context, t *ui.Theme, s model.Settings) {
	d := &p.atrust
	if !d.open {
		return
	}
	e := p.e
	p.mu.Lock()
	if d.pending != nil {
		d.form, d.pending = atrustFormFrom(*d.pending), nil
		d.session++
	}
	p.mu.Unlock()
	cfg, errs := d.form.parse()
	ui.Modal(c, &d.open, func() {
		ui.Column(c.Key(d.session)).Width(640).Gap(10).Children(func() {
			ui.Text(c, i18n.T("aTrust 兼容配置")).FontSize(15).Bold()
			ui.Text(c, i18n.T("每行一项。这些列表叠加在自动检测的结果之上；澜流不内置任何公司的地址，留空即不生效。「复制参考配置」里有每一项的说明。")).FontSize(12).TextColor(t.TextMuted)
			// domains and DNS on the left, ranges on the right
			ui.Row(c).Gap(16).AlignItems(ui.Start).Children(func() {
				ui.Column(c).Gap(10).Grow(1).Children(func() {
					p.atrustField(c, t, "corp", &d.form.corp, i18n.T("内网域名"), i18n.T("内网域名后缀：直接连接、不分配 fake-ip、不嗅探，由公司 DNS 解析"), errs[fieldCorp])
					p.atrustField(c, t, "direct", &d.form.direct, i18n.T("直连域名"), i18n.T("需要直连但不能走公司 DNS 的公网域名（如钉钉、企业微信），由公网 DNS 解析"), errs[fieldDirect])
					p.atrustField(c, t, "dns", &d.form.dns, i18n.T("公司 DNS"), i18n.T("公司 DNS 服务器的 IP；留空则使用检测到的 aTrust 解析器"), errs[fieldDNS])
				})
				ui.Column(c).Gap(10).Grow(1).Children(func() {
					p.atrustField(c, t, "exclude", &d.form.exclude, i18n.T("不进入 TUN 的网段"), i18n.T("交给系统路由（aTrust）处理并直接连接；切勿放入含 DNS 服务器的网段"), errs[fieldExclude])
					p.atrustField(c, t, "directcidr", &d.form.directCIDR, i18n.T("仅直连的网段"), i18n.T("直接连接但仍经过 TUN，发往其中 DNS 的查询照常被接管（例如含公司 DNS 的 10.0.0.0/8）"), errs[fieldDirectCIDR])
				})
			})
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Gap(2).Grow(1).Children(func() {
					ui.Text(c, i18n.T("未运行 aTrust 时也应用")).FontSize(13)
					ui.Text(c, i18n.T("在公司办公网不开 aTrust 时，内网域名仍需公司 DNS 解析")).FontSize(11).TextColor(t.TextMuted)
				})
				ui.Switch(c.Key("always"), &d.form.always).Label(i18n.T("未运行 aTrust 时也应用"))
			})
			ui.Row(c).Gap(8).Wrap().Children(func() {
				if ui.Button(c, i18n.T("导入…")).Tooltip(i18n.T("读取澜流的 YAML/JSON 或 JavaScript 覆写脚本（如 atrust-coexist.js），填入表单后再保存")).Clicked() {
					go p.importATrust()
				}
				if ui.Button(c, i18n.T("导出…")).Clicked() {
					go p.exportATrust()
				}
				if ui.Button(c, i18n.T("复制参考配置")).Clicked() {
					c.WriteClipboard(e.API.ATrustReference())
					e.Toast(i18n.T("参考配置已复制，粘贴到文本编辑器即可查看每一项的说明"), false)
				}
				if ui.Button(c, i18n.T("导出参考配置…")).Clicked() {
					go p.exportATrustReference()
				}
				ui.Spacer(c)
				if ui.Button(c, i18n.T("取消")).Clicked() {
					d.open = false
				}
				ui.PrimaryButton(c, i18n.T("保存")).Disabled(len(errs) > 0 || p.saving).OnClick(func() {
					s.ATrust = cfg
					d.open = false
					p.save(s, i18n.T("aTrust 兼容配置已保存"), nil)
				})
			})
		})
	})
}

// atrustField is one list of the sheet: label, hint, text and its error.
func (p *Page) atrustField(c *ui.Context, t *ui.Theme, key string, text *string, label, hint, errMsg string) {
	ui.Column(c).Gap(3).Children(func() {
		ui.Text(c, label).FontSize(13)
		ui.Text(c, hint).FontSize(11).TextColor(t.TextMuted)
		ui.TextArea(c.Key(key), text).Font("monospace").FontSize(12).Height(64).Label(label)
		if errMsg != "" {
			ui.Text(c, errMsg).FontSize(11).TextColor(t.Danger)
		}
	})
}

func atrustFileFilter() []mygo.FileFilter {
	return []mygo.FileFilter{{Name: i18n.T("aTrust 配置"), Extensions: []string{"yaml", "yml", "json", "js"}}}
}

// importATrust picks a file and fills the form with it; nothing is saved
// until 保存. The system dialog blocks, so this runs on its own goroutine.
func (p *Page) importATrust() {
	e := p.e
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Title:   i18n.T("导入 aTrust 配置"),
		Message: i18n.T("选择澜流导出的 aTrust 配置（YAML/JSON），或 JavaScript 覆写脚本（如 atrust-coexist.js）。内容会填入表单，确认后再保存。"),
		Filters: atrustFileFilter(),
	})
	if err != nil || len(paths) == 0 {
		return
	}
	e.Run(func() error {
		cfg, summary, err := e.API.ImportATrustConfig(paths[0])
		if err != nil {
			return err
		}
		p.mu.Lock()
		p.atrust.pending = &cfg
		p.toast = summary
		p.mu.Unlock()
		return nil
	}, "")
}

// exportATrust writes the saved lists to a file of the user's choice.
func (p *Page) exportATrust() {
	e := p.e
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: i18n.T("导出 aTrust 配置"), DefaultPath: "lanflow-atrust.yaml", Filters: atrustFileFilter()})
	if err != nil || path == "" {
		return
	}
	if filepath.Ext(path) == "" {
		path += ".yaml"
	}
	e.Run(func() error { return e.API.ExportATrustConfig(path) }, i18n.T("已导出到 ")+filepath.Base(path))
}

// exportATrustReference saves the reference template.
func (p *Page) exportATrustReference() {
	e := p.e
	path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{Title: i18n.T("导出 aTrust 参考配置"), DefaultPath: "atrust-reference.yaml", Filters: atrustFileFilter()})
	if err != nil || path == "" {
		return
	}
	if filepath.Ext(path) == "" {
		path += ".yaml"
	}
	e.Run(func() error {
		if err := os.WriteFile(path, []byte(e.API.ATrustReference()), 0o600); err != nil {
			return fmt.Errorf(i18n.T("写入 %s 失败：%v"), filepath.Base(path), err)
		}
		return nil
	}, i18n.T("已导出到 ")+filepath.Base(path))
}
