package env

import "github.com/egoist/mygo/ui"

// TwoColumnWidth is the window width from which pages lay their groups
// out in two columns (docs/design.md §4 布局原则).
const TwoColumnWidth = 1100

// TwoColumns reports whether the window is wide enough for two-column
// groups. Pages whose primary content is a pair of panels (网络, 概览)
// stay two-column at every size; this is for the secondary groups of
// 设置 and the like, which stack in one column below TwoColumnWidth.
func TwoColumns(c *ui.Context) bool {
	w, _ := c.Size()
	return w >= TwoColumnWidth
}

// Columns lays groups out in two equal columns when TwoColumns is true,
// in one column (left's groups, then right's) otherwise. gap separates
// the columns and the groups.
func Columns(c *ui.Context, gap float32, left, right func()) {
	if !TwoColumns(c) {
		ui.Column(c).Gap(gap).Children(func() {
			left()
			right()
		})
		return
	}
	ui.Row(c).Gap(gap).AlignItems(ui.Start).Children(func() {
		ui.Column(c).Grow(1).Basis(0).Gap(gap).Children(left)
		ui.Column(c).Grow(1).Basis(0).Gap(gap).Children(right)
	})
}
