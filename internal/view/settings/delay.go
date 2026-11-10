package settings

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// 延迟测试: the URL and timeout every delay test uses (Settings.DelayURL,
// DelayTimeoutMs) and 切换节点时断开该组的连接 (Settings.DisconnectOnSwitch),
// in 设置 › 代理.

const (
	minDelayMs = 1000
	maxDelayMs = 20000
)

// validDelayURL mirrors the service's check: an http(s) URL with a host.
func validDelayURL(u string) bool {
	u = strings.TrimSpace(u)
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return false
	}
	rest := u[strings.Index(u, "://")+3:]
	return rest != "" && !strings.HasPrefix(rest, "/")
}

func (p *Page) delaySection(c *ui.Context, t *ui.Theme, s model.Settings) {
	e := p.e
	e.Kit.Section(c, i18n.T("延迟测试"), i18n.T("代理页、代理组和菜单栏的测速都用这里的地址和超时"), func() {
		e.Kit.Row(c, i18n.T("测速地址"), i18n.T("节点经代理访问该地址，默认 www.gstatic.com/generate_204"), func() {
			ui.Column(c).Gap(4).AlignItems(ui.End).Children(func() {
				if p.delayURLErr == "" {
					p.delayURL.show(s.DelayURL)
				}
				in := ui.TextInput(c.Key("delay-url"), &p.delayURL.text).Width(300).Font("monospace").FontSize(12).
					Label(i18n.T("测速地址")).Invalid(p.delayURLErr != "")
				if in.Changed() {
					p.delayURLErr = ""
				}
				if p.delayURL.commit(in) {
					p.commitDelayURL(s)
				}
				if p.delayURLErr != "" {
					ui.Text(c, p.delayURLErr).FontSize(11).TextColor(t.Danger).MaxWidth(320).TextAlign(ui.End)
				}
			})
		})
		e.Kit.Row(c, i18n.T("测速超时"), i18n.Tf("毫秒，%d–%d；超过即显示为超时", minDelayMs, maxDelayMs), func() {
			ui.Column(c).Gap(4).AlignItems(ui.End).Children(func() {
				if p.delayMsErr == "" {
					p.delayMs.show(strconv.Itoa(s.DelayTimeoutMs))
				}
				in := ui.TextInput(c.Key("delay-ms"), &p.delayMs.text).Width(84).TextAlign(ui.End).FontFeatures("tnum").
					Label(i18n.T("测速超时")).Invalid(p.delayMsErr != "")
				if in.Changed() {
					p.delayMsErr = ""
				}
				if p.delayMs.commit(in) {
					p.commitDelayMs(s)
				}
				if p.delayMsErr != "" {
					ui.Text(c, p.delayMsErr).FontSize(11).TextColor(t.Danger).MaxWidth(320).TextAlign(ui.End)
				}
			})
		})
		p.switchRow(c, i18n.T("切换节点时断开该组的连接"), i18n.T("切换后立即断开经过该代理组的连接，让它们改走新节点"), s.DisconnectOnSwitch, func(on bool) {
			s.DisconnectOnSwitch = on
			p.save(s, "", nil)
		})
	})
}

func (p *Page) commitDelayURL(s model.Settings) {
	u := strings.TrimSpace(p.delayURL.text)
	if !validDelayURL(u) {
		p.delayURLErr = i18n.T("请输入以 http:// 或 https:// 开头的完整地址")
		return
	}
	if u == s.DelayURL {
		p.delayURLErr = ""
		return
	}
	s.DelayURL = u
	p.delayURLErr = ""
	p.save(s, i18n.T("已更新测速地址"), func(err error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.delayURLErr = err.Error()
	})
}

func (p *Page) commitDelayMs(s model.Settings) {
	n, err := strconv.Atoi(strings.TrimSpace(p.delayMs.text))
	if err != nil || n < minDelayMs || n > maxDelayMs {
		p.delayMsErr = i18n.Tf("超时须为 %d–%d 之间的毫秒数", minDelayMs, maxDelayMs)
		return
	}
	if n == s.DelayTimeoutMs {
		p.delayMsErr = ""
		return
	}
	s.DelayTimeoutMs = n
	p.delayMsErr = ""
	p.save(s, i18n.Tf("测速超时已改为 %d 毫秒", n), func(err error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.delayMsErr = err.Error()
	})
}
