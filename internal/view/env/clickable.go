package env

import "github.com/egoist/mygo/ui"

// Clickable makes el a button the keyboard reaches too: it takes the
// focus (Tab), is announced as a button named label (its text when label
// is ""), shows the pointer cursor, and its Clicked reports Enter or Space
// while focused as well as a click. For rows, cards and text links that
// act as buttons without looking like one.
func Clickable(el ui.Element, label string) ui.Element {
	el = el.Focusable().Role(ui.RoleButton).Cursor(ui.CursorPointer)
	if label != "" {
		el = el.Label(label)
	}
	return el
}

// ClickableRow is a ui.Row made Clickable.
func ClickableRow(c *ui.Context, label string) ui.Element { return Clickable(ui.Row(c), label) }

// ClickableColumn is a ui.Column made Clickable.
func ClickableColumn(c *ui.Context, label string) ui.Element {
	return Clickable(ui.Column(c), label)
}

// LinkText is a text in the accent colour that acts as a button.
func LinkText(c *ui.Context, text, label string) ui.Element {
	return Clickable(ui.Text(c, text).FontSize(12).TextColor(c.Theme().Accent).SingleLine(), label)
}
