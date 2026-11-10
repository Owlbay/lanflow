package env

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestClickableKeyboard: a ClickableRow takes the focus with Tab and acts
// on Enter and Space, as a button does.
func TestClickableKeyboard(t *testing.T) {
	n := 0
	tt := ui.NewTester(func(c *ui.Context) {
		r := ClickableRow(c, "打开")
		r.Children(func() { ui.Text(c, "行") })
		if r.Clicked() {
			n++
		}
	}, 300, 200)
	if info, ok := tt.AccessByRole(ui.RoleButton, "打开"); !ok {
		t.Fatalf("no button named 打开: %+v", info)
	}
	tt.Key(0, ui.KeyTab)
	tt.Key(0, ui.KeyEnter)
	tt.Key(0, ui.KeySpace)
	tt.Frame()
	if n != 2 {
		t.Fatalf("Enter and Space clicked %d times, want 2", n)
	}
}
