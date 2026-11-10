package env

import (
	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
)

// SettingsPlace names the Settings tab key (settings tabKeys) as the user
// reads it, 设置 › 代理, for links that send them there.
func SettingsPlace(key string) string {
	switch key {
	case "proxy":
		return i18n.T("设置 › 代理")
	case "dns":
		return i18n.T("设置 › DNS")
	case "sniffer":
		return i18n.T("设置 › 嗅探")
	case "advanced":
		return i18n.T("设置 › 高级")
	}
	return Title(Settings)
}

// GoLink is a small link to where the status it follows is configured:
// page, opened with arg (a Settings tab key for Settings). text is its
// words and label its accessible name ("" = text); the tooltip names the
// destination. A click navigates there.
func (e *Env) GoLink(c *ui.Context, text, label string, page Page, arg string) ui.Element {
	place := Title(page)
	if page == Settings {
		place = SettingsPlace(arg)
	}
	l := LinkText(c, text, label).FontSize(11).Shrink(0).Tooltip(i18n.Tf("前往%s", place))
	if l.Clicked() {
		e.Navigate(page, arg)
	}
	return l
}
