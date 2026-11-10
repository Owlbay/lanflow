package settings

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// 按 Wi-Fi 切换订阅 (Settings.SSIDRules, SSIDDefaultSub), in 设置 › 代理:
// one row per rule (the network and the subscription it picks, 删除), a
// row to add one (「使用当前 Wi-Fi」 fills the SSID the 网络 page shows),
// and the subscription for networks without a rule. The service applies
// the rules when the network changes (app/wifi.go).

const wifiNoSwitch = "（不切换）"

func (p *Page) wifiSection(c *ui.Context, t *ui.Theme, s model.Settings) {
	e := p.e
	subs := e.API.Subscriptions()
	names := make([]string, 0, len(subs))
	byName := map[string]string{}
	byID := map[string]string{}
	for _, sub := range subs {
		names = append(names, sub.Name)
		byName[sub.Name] = sub.ID
		byID[sub.ID] = sub.Name
	}
	note := i18n.T("连接到某个 Wi-Fi 时自动切换到对应的订阅")
	e.Kit.Section(c, i18n.T("按 Wi-Fi 切换订阅"), note, func() {
		for i, r := range s.SSIDRules {
			i, r := i, r
			subName := byID[r.SubscriptionID]
			desc := ""
			if subName == "" {
				subName = i18n.T(wifiNoSwitch)
				desc = i18n.T("对应的订阅已不存在")
			}
			e.Kit.Row(c, r.SSID, desc, func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					v := subName
					if ui.Select(c.Key("wifi-sub-"+strconv.Itoa(i)), &v, append([]string{i18n.T(wifiNoSwitch)}, names...)).Label(i18n.T("订阅")).Changed() {
						rules := append([]model.SSIDRule(nil), s.SSIDRules...)
						rules[i].SubscriptionID = byName[v]
						s.SSIDRules = rules
						p.save(s, "", nil)
					}
					ui.Button(c, i18n.T("删除")).Disabled(p.saving).OnClick(func() {
						rules := append([]model.SSIDRule(nil), s.SSIDRules[:i]...)
						s.SSIDRules = append(rules, s.SSIDRules[i+1:]...)
						p.save(s, i18n.Tf("已删除 Wi-Fi「%s」的规则", r.SSID), nil)
					})
				})
			})
		}
		cur := strings.TrimSpace(e.API.NetInfo().SSID)
		addDesc := i18n.Tf("当前 Wi-Fi：%s", cur)
		if cur == "" {
			addDesc = i18n.T("当前 Wi-Fi 名称不可用")
			if e.Mac {
				addDesc = i18n.T("当前 Wi-Fi 名称不可用：macOS 需要定位权限才能读取 Wi-Fi 名称（系统设置 › 隐私与安全性 › 定位服务）")
			}
		}
		// The add row stacks its controls under the label: a button, a
		// text field and a select do not fit beside it in a half column.
		ui.Column(c.Key("wifi-add-"+strconv.Itoa(p.f2.wifiAdded))).Gap(8).Padding(8, 12).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, i18n.T("添加规则")).FontSize(13)
				ui.Text(c, addDesc).FontSize(11).TextColor(t.TextMuted)
			})
			ui.Row(c).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
				if cur != "" {
					ui.Button(c, i18n.T("使用当前 Wi-Fi")).OnClick(func() { p.f2.wifiSSID = cur; p.f2.wifiErr = "" })
				}
				in := ui.TextInput(c.Key("wifi-ssid"), &p.f2.wifiSSID).Width(160).Placeholder(i18n.T("Wi-Fi 名称（SSID）")).Label(i18n.T("Wi-Fi 名称（SSID）"))
				if in.Changed() {
					p.f2.wifiErr = ""
				}
				if p.f2.wifiSub == "" && len(names) > 0 {
					p.f2.wifiSub = names[0]
				}
				if len(names) > 0 {
					ui.Select(c.Key("wifi-add-sub"), &p.f2.wifiSub, names).Label(i18n.T("订阅"))
				}
				ui.Button(c, i18n.T("添加")).Disabled(p.saving || len(names) == 0).OnClick(func() { p.addWiFiRule(s, byName) })
				if in.Submitted() {
					p.addWiFiRule(s, byName)
				}
			})
			if p.f2.wifiErr != "" {
				ui.Text(c, p.f2.wifiErr).FontSize(11).TextColor(t.Danger)
			}
		})
		def := byID[s.SSIDDefaultSub]
		if def == "" {
			def = i18n.T(wifiNoSwitch)
		}
		e.Kit.Row(c, i18n.T("其他网络"), i18n.T("连接到没有规则的 Wi-Fi 时使用的订阅；有线或无法读取名称的网络不切换"), func() {
			v := def
			if ui.Select(c.Key("wifi-default"), &v, append([]string{i18n.T(wifiNoSwitch)}, names...)).Label(i18n.T("其他网络")).Changed() {
				s.SSIDDefaultSub = byName[v]
				p.save(s, "", nil)
			}
		})
	})
}

// addWiFiRule validates the add form and saves the rule.
func (p *Page) addWiFiRule(s model.Settings, byName map[string]string) {
	ssid := strings.TrimSpace(p.f2.wifiSSID)
	if ssid == "" {
		p.f2.wifiErr = i18n.T("请填写 Wi-Fi 名称。")
		return
	}
	id := byName[p.f2.wifiSub]
	if id == "" {
		p.f2.wifiErr = i18n.T("请选择一个订阅。")
		return
	}
	for _, r := range s.SSIDRules {
		if strings.EqualFold(strings.TrimSpace(r.SSID), ssid) {
			p.f2.wifiErr = i18n.Tf("Wi-Fi「%s」已有规则。", r.SSID)
			return
		}
	}
	s.SSIDRules = append(append([]model.SSIDRule(nil), s.SSIDRules...), model.SSIDRule{SSID: ssid, SubscriptionID: id})
	p.f2.wifiSSID, p.f2.wifiErr = "", ""
	p.f2.wifiAdded++
	p.save(s, i18n.Tf("连接 Wi-Fi「%s」时将切换到「%s」", ssid, p.f2.wifiSub), nil)
}
