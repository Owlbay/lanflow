package view

import (
	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/view/env"
)

// kit is the real env.Kit: the native-looking building blocks every page
// shares (docs/design.md §3).
type kit struct{}

// NewKit returns the shared component kit.
func NewKit() env.Kit { return kit{} }

// statusColor maps a status kind to the theme's color.
func statusColor(t *ui.Theme, kind string) ui.Color {
	switch kind {
	case "green":
		return t.Success
	case "orange":
		return t.Warning
	case "red":
		return t.Danger
	case "accent":
		return t.Accent
	default:
		return t.TextMuted
	}
}

// Header draws the page title row: 20 pt semibold title, muted subtitle,
// tools right-aligned. It stays put while the content below scrolls, and
// its background drags the window (the title bar is hidden on macOS);
// the tools in it keep their own clicks.
func (kit) Header(c *ui.Context, title, subtitle string, tools func()) {
	t := c.Theme()
	ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(20, 24, 12, 24).Shrink(0).MinHeight(60).DragWindow().Children(func() {
		ui.Text(c, title).FontSize(20).FontWeight(600).SingleLine().Shrink(0)
		if subtitle != "" {
			ui.Text(c, subtitle).FontSize(13).TextColor(t.TextMuted).SingleLine().Margin(0, 0, 0, 4).FontFeatures("tnum")
		}
		ui.Spacer(c)
		if tools != nil {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(tools)
		}
	})
}

// Section draws a group heading and a rounded box whose rows are
// separated by hairlines.
func (kit) Section(c *ui.Context, title, note string, body func()) {
	t := c.Theme()
	ui.Column(c).Gap(6).Shrink(0).Children(func() {
		if title != "" || note != "" {
			ui.Row(c).Gap(8).AlignItems(ui.Baseline).Padding(0, 4).Children(func() {
				if title != "" {
					ui.Text(c, title).FontSize(13).FontWeight(600).SingleLine().Shrink(0)
				}
				if note != "" {
					ui.Text(c, note).FontSize(11).TextColor(t.TextMuted).Shrink(1)
				}
			})
		}
		GroupBox(c).Children(body)
	})
}

// GroupBox returns the rounded group container pages put rows in; its
// children are separated by hairlines.
func GroupBox(c *ui.Context) ui.Element {
	t := c.Theme()
	return ui.Column(c).Radius(10).Background(t.Surface).Border(1, t.Border).Clip().Dividers(1, t.Border)
}

// Row draws one settings-style row: label (+ muted description) on the
// left, control on the right.
func (kit) Row(c *ui.Context, label, desc string, control func()) {
	t := c.Theme()
	ui.Row(c).MinHeight(40).Padding(6, 12).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(2).Shrink(1).Children(func() {
			ui.Text(c, label).FontSize(13)
			if desc != "" {
				ui.Text(c, desc).FontSize(11).TextColor(t.TextMuted)
			}
		})
		ui.Spacer(c)
		if control != nil {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(control)
		}
	})
}

// Empty draws a centered empty state.
func (k kit) Empty(c *ui.Context, icon, title, desc string, actions func()) {
	t := c.Theme()
	// Vertical padding stays small so the block also fits a short panel
	// (the leak-check card at the minimum window height).
	ui.Column(c).Grow(1).Shrink(1).Center().Gap(8).Padding(12, 24).Children(func() {
		if icon != "" {
			ui.Box(c).Margin(0, 0, 6, 0).TextColor(t.TextMuted).Children(func() { k.Icon(c, icon, 28) })
		}
		ui.Text(c, title).FontSize(15).FontWeight(600).TextAlign(ui.Center)
		if desc != "" {
			ui.Text(c, desc).FontSize(13).TextColor(t.TextMuted).TextAlign(ui.Center).MaxWidth(440)
		}
		if actions != nil {
			ui.Row(c).Gap(8).Margin(10, 0, 0, 0).AlignItems(ui.Center).Children(actions)
		}
	})
}

// Banner draws an inline message bar; kind is "info", "warn" or "error".
func (k kit) Banner(c *ui.Context, kind, msg string, actions func()) {
	t := c.Theme()
	col, icon := t.Accent, "info"
	switch kind {
	case "warn":
		col, icon = t.Warning, "warn"
	case "error":
		col, icon = t.Danger, "warn"
	}
	ui.Row(c).Gap(10).Padding(8, 12).Radius(8).Background(col.Alpha(0.10)).Border(1, col.Alpha(0.35)).
		AlignItems(ui.Center).Shrink(0).Role(ui.RoleStatus).Children(func() {
		ui.Box(c).TextColor(col).Shrink(0).Children(func() { k.Icon(c, icon, 16) })
		ui.Text(c, msg).FontSize(13).Grow(1)
		if actions != nil {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(actions)
		}
	})
}

// Tag draws a small pill; kind is "accent", "gray", "green", "orange", "red".
func (kit) Tag(c *ui.Context, text, kind string) {
	col := statusColor(c.Theme(), kind)
	ui.Text(c, text).FontSize(11).Padding(1, 6).Radius(999).Background(col.Alpha(0.14)).TextColor(col).SingleLine().Shrink(0)
}

// Delay draws a latency label colored by env.DelayKind, a spinner while testing.
func (kit) Delay(c *ui.Context, ms int, testing bool) {
	if testing {
		ui.Spinner(c).Label(i18n.T("测速中")).Size(14, 14)
		return
	}
	col := statusColor(c.Theme(), env.DelayKind(ms))
	ui.Text(c, env.FmtDelay(ms, false)).FontSize(12).TextColor(col).FontFeatures("tnum").SingleLine().Shrink(0)
}

// Dot draws an 8 pt status dot.
func (kit) Dot(c *ui.Context, kind string) {
	ui.Box(c).Size(8, 8).Radius(4).Background(statusColor(c.Theme(), kind)).Shrink(0)
}

// Icon draws a monochrome line icon by name, in the text color.
func (kit) Icon(c *ui.Context, name string, size float32) {
	s := icons[name]
	if s == nil {
		return
	}
	ui.Icon(c, s).Size(size, size).Shrink(0)
}
