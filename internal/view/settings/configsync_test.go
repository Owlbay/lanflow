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
