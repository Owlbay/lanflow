package logs

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// logAPI serves a fixed log so tests do not depend on the fake's random lines.
type logAPI struct {
	*fake.Fake
	logs []model.LogEntry
}

func (a *logAPI) Logs() []model.LogEntry { return append([]model.LogEntry(nil), a.logs...) }
func (a *logAPI) ClearLogs()             { a.logs = nil }

type rec struct{ toasts []string }

func testEnv(api *logAPI) (*env.Env, *rec) {
	r := &rec{}
	e := &env.Env{API: api, Kit: env.Basic(), Mac: true,
		Navigate: func(env.Page, string) {},
		Arg:      func(env.Page) string { return "" },
		Toast:    func(m string, bad bool) { r.toasts = append(r.toasts, m) },
		Run: func(job func() error, ok string) {
			if err := job(); err != nil {
				r.toasts = append(r.toasts, err.Error())
			} else if ok != "" {
				r.toasts = append(r.toasts, ok)
			}
		},
		Confirm: func(title, body, okLabel string, danger bool, onOK func()) { onOK() },
		Redraw:  func() {},
	}
	return e, r
}

func entries(n int) []model.LogEntry {
	base := time.Date(2026, 11, 8, 10, 21, 0, 0, time.Local)
	var out []model.LogEntry
	for i := 0; i < n; i++ {
		l := model.LogEntry{Time: base.Add(time.Duration(i) * time.Second), Level: "info", Payload: fmt.Sprintf("[TCP] 127.0.0.1:%d --> www.google.com:443 match DomainSuffix using 节点选择[东京 03]", 52000+i)}
		switch i % 10 {
		case 3:
			l.Level, l.Payload = "warning", fmt.Sprintf("[DNS] resolve api%d.example.com failed: i/o timeout", i)
		case 7:
			l.Level, l.Payload = "error", fmt.Sprintf("proxy 美国 05 health check failed #%d", i)
		}
		out = append(out, l)
	}
	return out
}

func TestLevelFilterAndSearch(t *testing.T) {
	api := &logAPI{Fake: fake.NewInstant(), logs: entries(20)}
	e, _ := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 700)
	if !tt.HasText("[TCP]") || !tt.HasText("[DNS]") {
		t.Fatalf("log rows missing: %q", tt.Texts())
	}
	// The filter starts at 信息 (docs/design.md §5.5).
	if p.level != 1 || !tt.HasText("级别：信息") {
		t.Fatalf("default level %d, texts %q", p.level, tt.Texts())
	}
	// Level: 错误 hides info and warning lines.
	if err := tt.Click("级别：信息"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("级别：错误"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("[TCP]") || tt.HasText("[DNS]") || !tt.HasText("health check failed") {
		t.Fatalf("level filter: %q", tt.Texts())
	}
	// Back to 调试 and search.
	if err := tt.Click("级别：错误"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("级别：调试"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("搜索日志"); err != nil {
		t.Fatal(err)
	}
	tt.Type("api13")
	for _, s := range tt.Texts() {
		if strings.Contains(s, "[TCP]") {
			t.Fatalf("search did not filter: %q", s)
		}
	}
	if !tt.HasText("api13.example.com") {
		t.Fatalf("search result missing: %q", tt.Texts())
	}
	tt.Type("zz")
	if !tt.HasText("没有匹配“api13zz”的日志") {
		t.Fatalf("no-match state: %q", tt.Texts())
	}
}

func TestPauseClearAndCopy(t *testing.T) {
	api := &logAPI{Fake: fake.NewInstant(), logs: entries(5)}
	e, r := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 700)
	if err := tt.Click("暂停"); err != nil {
		t.Fatal(err)
	}
	api.logs = append(api.logs, model.LogEntry{Time: time.Now(), Level: "error", Payload: "NEW LINE"})
	tt.Frame()
	if tt.HasText("NEW LINE") {
		t.Fatal("paused list should not show new lines")
	}
	if err := tt.Click("继续"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("NEW LINE") {
		t.Fatal("resumed list should show new lines")
	}
	if err := tt.Click("⋯"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("复制全部"); err != nil {
		t.Fatal(err)
	}
	if clip := tt.Clipboard(); !strings.Contains(clip, "NEW LINE") || !strings.Contains(clip, "10:21:00 信息 [TCP]") {
		t.Fatalf("clipboard: %q", clip)
	}
	if len(r.toasts) == 0 || r.toasts[len(r.toasts)-1] != "已复制" {
		t.Fatalf("toasts: %q", r.toasts)
	}
	if err := tt.Click("清空"); err != nil {
		t.Fatal(err)
	}
	if len(api.logs) != 0 || !tt.HasText("暂无日志") {
		t.Fatalf("clear: %d logs, texts %q", len(api.logs), tt.Texts())
	}
}

func TestFollowBottom(t *testing.T) {
	api := &logAPI{Fake: fake.NewInstant(), logs: entries(400)}
	e, _ := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 600)
	if !p.list.AtEnd() {
		t.Fatal("the list should start at its end")
	}
	if tt.HasText("回到底部") {
		t.Fatal("no 回到底部 while following the end")
	}
	// Scroll up: the button appears; new lines mark it.
	tt.Scroll(500, 300, 0, -2000)
	tt.Frame()
	if p.list.AtEnd() || !tt.HasText("回到底部") {
		t.Fatalf("after scrolling up: atEnd=%v texts=%q", p.list.AtEnd(), tt.Texts()[:5])
	}
	api.logs = append(api.logs, model.LogEntry{Time: time.Now(), Level: "info", Payload: "later"})
	tt.Frame()
	if !tt.HasText("回到底部（有新日志）") {
		t.Fatalf("new-log hint missing: %q", tt.Texts()[:6])
	}
	if err := tt.Click("回到底部（有新日志）"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !p.list.AtEnd() || tt.HasText("回到底部") {
		t.Fatal("回到底部 should scroll to the end and hide")
	}
}

// TestCopyAllIgnoresFilter: 「复制全部」 copies every line held, not only the
// lines the level filter and the search show (docs/design.md §5.5).
func TestCopyAllIgnoresFilter(t *testing.T) {
	api := &logAPI{Fake: fake.NewInstant(), logs: entries(20)}
	e, _ := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 700)
	if err := tt.Click("级别：信息"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("级别：错误"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("搜索日志"); err != nil {
		t.Fatal(err)
	}
	tt.Type("#17")
	tt.Frame()
	if tt.HasText("[TCP]") || tt.HasText("#7") {
		t.Fatalf("filter not applied: %q", tt.Texts())
	}
	if err := tt.Click("⋯"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("复制全部"); err != nil {
		t.Fatal(err)
	}
	clip := tt.Clipboard()
	if n := strings.Count(clip, "\n"); n != 20 {
		t.Fatalf("copied %d lines, want all 20:\n%s", n, clip)
	}
	if !strings.Contains(clip, "信息 [TCP]") || !strings.Contains(clip, "警告 [DNS]") || !strings.Contains(clip, "错误 proxy") {
		t.Fatalf("clipboard lacks hidden lines:\n%s", clip)
	}
}

// TestLineColors: a warning or error line is coloured as a whole, time
// column included; other lines keep the default colour with a muted time.
func TestLineColors(t *testing.T) {
	th := ui.LightTheme()
	if text, tm := lineColors("warning", th); text != th.Warning || tm != th.Warning {
		t.Errorf("warning colours %v %v", text, tm)
	}
	if text, tm := lineColors("error", th); text != th.Danger || tm != th.Danger {
		t.Errorf("error colours %v %v", text, tm)
	}
	if text, tm := lineColors("info", th); text != th.Text || tm != th.TextMuted {
		t.Errorf("info colours %v %v", text, tm)
	}
}
