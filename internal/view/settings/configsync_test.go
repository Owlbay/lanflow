package settings

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
)

// TestSyncChoiceDialog: turning sync on beside a different lanflow.yaml
// asks which side wins and passes the answer on; nothing is decided
// without it.
func TestSyncChoiceDialog(t *testing.T) {
	f := fake.NewInstant()
	f.Sync().NeedChoice = true
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 1400)
	if !tt.HasText("配置同步") || !tt.HasText("同步位置") {
		t.Fatalf("section missing: %q", tt.Texts())
	}
	p.setSync(model.SyncFolder, "/Users/demo/OneDrive/Lanflow", "")
	tt.Frame()
	if !tt.HasText("同步文件夹中已有配置") {
		t.Fatalf("choice dialog not shown: %q (toasts %q)", tt.Texts(), r.toasts)
	}
	if f.Settings().Sync.Mode != model.SyncOff {
		t.Fatal("sync turned on before the choice")
	}
	if err := tt.Click("使用文件中的配置"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	ch := f.Sync().Choices
	if len(ch) != 2 || ch[1] != model.SyncUseFile || f.Settings().Sync.Dir != "/Users/demo/OneDrive/Lanflow" {
		t.Fatalf("choices %q, sync %+v", ch, f.Settings().Sync)
	}
	if tt.HasText("同步文件夹中已有配置") {
		t.Fatal("dialog still open")
	}
	tt.Frame()
	if !tt.HasText("同步文件") || !tt.HasText("更改…") {
		t.Fatalf("on state not shown: %q", tt.Texts())
	}
}

// TestSyncBanners: a refused change shows its line; conflict copies
// offer both choices, each confirmed.
func TestSyncBanners(t *testing.T) {
	f := fake.NewInstant()
	f.Sync().Status = &model.ConfigSyncStatus{
		Mode: model.SyncFolder, Dir: "/d", Path: "/d/lanflow.yaml", State: model.SyncStateError,
		Error: "mihomo -t: bad rule", ErrorLine: 42, Conflicts: []string{"lanflow 2.yaml"},
		MissingSecrets: []string{"机场 A"},
	}
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 1600)
	joined := strings.Join(tt.Texts(), "\n")
	for _, want := range []string{"第 42 行", "bad rule", "lanflow 2.yaml", "机场 A"} {
		if !strings.Contains(joined, want) {
			t.Errorf("banner text %q missing: %q", want, tt.Texts())
		}
	}
	if err := tt.Click("保留此副本"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(r.confirms) != 1 || len(f.Sync().Resolved) != 1 || f.Sync().Resolved[0] != "lanflow 2.yaml:copy" {
		t.Fatalf("confirms %q resolved %q", r.confirms, f.Sync().Resolved)
	}
}

// TestSyncPassphraseSet: the switch opens the passphrase dialog; a short
// or mismatched passphrase stays in the dialog, a good one turns sealing
// on and closes it.
func TestSyncPassphraseSet(t *testing.T) {
	f := fake.NewInstant()
	f.Sync().Status = &model.ConfigSyncStatus{Mode: model.SyncFolder, Dir: "/d", Path: "/d/lanflow.yaml", State: model.SyncStateOK}
	e, r := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 1600)
	if !tt.HasText("用口令加密链接") || !strings.Contains(strings.Join(tt.Texts(), "\n"), "口令丢失后无法恢复") {
		t.Fatalf("row or its warning missing: %q", tt.Texts())
	}
	clickRole(t, tt, ui.RoleSwitch, "用口令加密链接")
	tt.Frame()
	if !tt.HasText("设置同步口令") || !tt.HasText("再次输入口令") {
		t.Fatalf("dialog not open: %q", tt.Texts())
	}
	try := func(pass, confirm string) {
		p.mu.Lock()
		p.sync.pass.pass, p.sync.pass.confirm = pass, confirm
		p.mu.Unlock()
		p.submitPass()
		tt.Frame()
	}
	try("short", "short")
	if !tt.HasText("同步口令至少需要 8 个字符。") || len(f.Sync().Calls) != 0 {
		t.Fatalf("short passphrase: %q calls %q", tt.Texts(), f.Sync().Calls)
	}
	try("long enough one", "long enough two")
	if !tt.HasText("两次输入的口令不一致。") || len(f.Sync().Calls) != 0 {
		t.Fatalf("mismatch: %q", tt.Texts())
	}
	try("long enough one", "long enough one")
	tt.Frame()
	if c := f.Sync().Calls; len(c) != 1 || c[0] != "set" {
		t.Fatalf("calls %q", c)
	}
	if tt.HasText("设置同步口令") {
		t.Fatal("dialog still open after success")
	}
	if !strings.Contains(strings.Join(r.toasts, "\n"), "已用口令加密") {
		t.Fatalf("toasts %q", r.toasts)
	}
	if !tt.HasText("修改口令…") || !tt.HasText("在本机忘记口令") {
		t.Fatalf("change / forget not offered: %q", tt.Texts())
	}
	if err := tt.Click("在本机忘记口令"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if c := f.Sync().Calls; len(c) != 2 || c[1] != "forget" || len(r.confirms) != 1 {
		t.Fatalf("forget: calls %q confirms %q", c, r.confirms)
	}
}

// TestSyncPassphraseUnlock: a locked device shows the banner; a wrong
// passphrase says so in the dialog, the right one unlocks.
func TestSyncPassphraseUnlock(t *testing.T) {
	f := fake.NewInstant()
	f.Sync().Status = &model.ConfigSyncStatus{Mode: model.SyncFolder, Dir: "/d", Path: "/d/lanflow.yaml", State: model.SyncStateOK,
		Encrypted: true, Locked: true, LockedSecrets: 2}
	f.Sync().Passphrase = "the right one"
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 1600)
	if !strings.Contains(strings.Join(tt.Texts(), "\n"), "输入同步口令以解锁订阅链接") {
		t.Fatalf("banner missing: %q", tt.Texts())
	}
	if tt.HasText("修改口令…") {
		t.Fatal("change offered while locked")
	}
	if err := tt.Click("输入口令…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("输入同步口令") || tt.HasText("再次输入口令") {
		t.Fatalf("unlock dialog: %q", tt.Texts())
	}
	p.mu.Lock()
	p.sync.pass.pass = "a wrong one"
	p.mu.Unlock()
	p.submitPass()
	tt.Frame()
	if !tt.HasText("同步口令不正确，请重新输入。") || !tt.HasText("输入同步口令") {
		t.Fatalf("wrong passphrase not shown: %q", tt.Texts())
	}
	p.mu.Lock()
	p.sync.pass.pass = "the right one"
	p.mu.Unlock()
	p.submitPass()
	tt.Frame()
	tt.Frame()
	if tt.HasText("输入同步口令") || strings.Contains(strings.Join(tt.Texts(), "\n"), "解锁订阅链接（") {
		t.Fatalf("still locked: %q", tt.Texts())
	}
	if !tt.HasText("修改口令…") {
		t.Fatalf("unlocked state not shown: %q", tt.Texts())
	}
}
