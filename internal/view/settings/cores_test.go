package settings

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
)

// The 内核版本 card lists the bundled core and every installed version;
// 安装其他版本… lists the releases of a channel and installs one; 使用
// switches and 删除 removes (never the active or the bundled one).
func TestCoreVersionsCard(t *testing.T) {
	f := fake.NewInstant()
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 900)
	if err := tt.Click("内核"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{"内核版本", "Mihomo 版本", "内置 · v1.19.33", "使用中", "随应用安装", "安装其他版本…", "内核更新", "自动检查更新", "更新通道", "自动安装更新"} {
		if !tt.HasText(want) {
			t.Fatalf("missing %q in %q", want, tt.Texts())
		}
	}
	// the bundled core is active: nothing to switch to or remove yet
	if _, ok := tt.AccessByRole(ui.RoleButton, "删除"); ok {
		t.Fatal("the bundled core must not offer 删除")
	}

	// 安装其他版本…: the stable releases with notes, then the alpha one
	if err := tt.Click("安装其他版本…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !p.cores.installOpen || !tt.HasText("安装其他版本") || !tt.HasText("v1.19.34") || !tt.HasText("fix: DNS fallback filter") {
		t.Fatalf("dialog: %q", tt.Texts())
	}
	if err := tt.Click("安装"); err != nil { // the first row: v1.19.34
		t.Fatal(err)
	}
	tt.Frame()
	if len(r.toasts) == 0 || !strings.HasPrefix(r.toasts[len(r.toasts)-1], "内核 v1.19.34 已安装") {
		t.Fatalf("install toast: %q", r.toasts)
	}
	if !tt.HasText("已安装") {
		t.Fatalf("installed release should be marked: %q", tt.Texts())
	}
	if err := tt.Click("预发布"); err != nil { // the dialog's channel control
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("alpha-5a3a0ef") {
		t.Fatalf("alpha list: %q", tt.Texts())
	}
	if err := tt.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if p.cores.installOpen || !tt.HasText("v1.19.34") || !tt.HasText("正式版 · ") {
		t.Fatalf("after close: %q", tt.Texts())
	}

	// 使用 switches to v1.19.34; the active row then offers no 删除
	if err := tt.Click("使用"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if f.Settings().CoreVersion != "v1.19.34" || r.toasts[len(r.toasts)-1] != "已切换到内核 v1.19.34" {
		t.Fatalf("switch: %q %q", f.Settings().CoreVersion, r.toasts)
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "删除"); ok {
		t.Fatal("the active version must not offer 删除")
	}

	// back to the bundled core, then remove v1.19.34 after confirming
	if err := tt.Click("使用"); err != nil { // the bundled row is first
		t.Fatal(err)
	}
	tt.Frame()
	if f.Settings().CoreVersion != "" || r.toasts[len(r.toasts)-1] != "已恢复内置内核" {
		t.Fatalf("bundled: %q %q", f.Settings().CoreVersion, r.toasts)
	}
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(r.confirms) == 0 || r.confirms[len(r.confirms)-1] != "删除内核 v1.19.34？" {
		t.Fatalf("confirms: %q", r.confirms)
	}
	if tt.HasText("v1.19.34") || r.toasts[len(r.toasts)-1] != "已删除内核 v1.19.34" {
		t.Fatalf("remove: %q %q", tt.Texts(), r.toasts)
	}

	// update settings apply at once
	clickRole(t, tt, ui.RoleSwitch, "自动安装更新")
	tt.Frame()
	if !f.Settings().CoreAutoInstall {
		t.Fatal("CoreAutoInstall not saved")
	}
	chooseOption(t, tt, "更新通道", "预发布")
	if f.Settings().CoreChannel != "alpha" {
		t.Fatalf("CoreChannel = %q", f.Settings().CoreChannel)
	}
}
