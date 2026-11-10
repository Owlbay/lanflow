package env

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/geo"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// Small helpers several pages share.

// Dash returns s, or "—" for an empty value.
func Dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// FlagPrefix returns the flag of a country code followed by a space, or
// "" for an unknown code, to put before a place's name.
func FlagPrefix(cc string) string {
	if f := geo.Flag(cc); f != "" {
		return f + " "
	}
	return ""
}

// ModeName names a proxy mode: 规则, 全局 or 直连.
func ModeName(m model.Mode) string {
	switch m {
	case model.ModeGlobal:
		return i18n.T("全局")
	case model.ModeDirect:
		return i18n.T("直连")
	default:
		return i18n.T("规则")
	}
}

// Highlight draws text in color with the case-insensitive matches of q
// marked, for the rows a search filters.
func Highlight(c *ui.Context, text, q string, color ui.Color) ui.Element {
	if q == "" {
		return ui.Text(c, text).TextColor(color)
	}
	mark := c.Theme().Warning.Alpha(0.35)
	lower, lq := strings.ToLower(text), strings.ToLower(q)
	if len(lower) != len(text) {
		// A case change altered the byte length (rare letters): mark nothing
		// rather than cut a character apart.
		return ui.Text(c, text).TextColor(color)
	}
	var spans []ui.Span
	for i := 0; i < len(text); {
		j := strings.Index(lower[i:], lq)
		if j < 0 {
			spans = append(spans, ui.Span{Text: text[i:], Color: color})
			break
		}
		if j > 0 {
			spans = append(spans, ui.Span{Text: text[i : i+j], Color: color})
		}
		spans = append(spans, ui.Span{Text: text[i+j : i+j+len(lq)], Color: color, Background: mark})
		i += j + len(lq)
	}
	return ui.RichText(c, spans...)
}

// InfoCard is a rounded card sharing its row with others: a bold title
// with right (tags, a spinner, a link) at its end, then body.
func InfoCard(c *ui.Context, title string, right func(), body func()) {
	t := c.Theme()
	ui.Column(c).Grow(1).Basis(0).Padding(14).Gap(10).Radius(10).Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).Bold().FontSize(13).Grow(1)
			if right != nil {
				right()
			}
		})
		body()
	})
}

// KV is a key-value line: the key muted in a column labelW wide, the
// value (if any) selectable, and extra after it. A oneLine value is cut
// to one line; otherwise it wraps.
func KV(c *ui.Context, k, v string, labelW float32, oneLine bool, extra func()) {
	t := c.Theme()
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		key := ui.Text(c, k).Width(labelW).Shrink(0).FontSize(12).TextColor(t.TextMuted)
		if oneLine {
			key.SingleLine()
		}
		if v != "" {
			val := ui.Text(c, v).FontSize(12).FontFeatures("tnum").Selectable()
			if oneLine {
				val.SingleLine().Shrink(1)
			}
		}
		if extra != nil {
			extra()
		}
	})
}
