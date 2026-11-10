package settings

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
)

// TestCacheRowShowsSizesAndCleans: 设置 › 数据 lists the caches with their
// sizes; 「清理缓存」 removes them, toasts what was freed and the row shows
// what is left.
func TestCacheRowShowsSizesAndCleans(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 800)
	tt.Frame()
	if !tt.HasText("图标 1.2 MB · 内核版本列表 18.0 KB · 内核缓存 128.0 KB · 旧日志 5.0 MB · 共 6.3 MB") {
		t.Fatalf("cache sizes missing: %q", tt.Texts())
	}
	clickRole(t, tt, ui.RoleButton, "清理缓存")
	tt.Frame()
	if len(r.toasts) != 1 || !strings.HasPrefix(r.toasts[0], "已清理 6.2 MB") {
		t.Fatalf("toasts after cleaning: %q", r.toasts)
	}
	if !tt.HasText("内核缓存 128.0 KB · 共 128.0 KB") {
		t.Errorf("the row should show what is left: %q", tt.Texts())
	}
}
