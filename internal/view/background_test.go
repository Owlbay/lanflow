package view

import (
	"strings"
	"sync"
	"testing"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/app/fake"
	"lanflow/internal/model"
)

// The service process asks for agents' consent with a notification and a
// tray item, each opening a dialog; no window is involved.
func TestBackgroundConsent(t *testing.T) {
	b := NewBackground(fake.NewInstant(), BackgroundOptions{})
	var notified []string
	var clicks []func()
	b.notify = func(title, body string, onClick func()) {
		notified = append(notified, title+"|"+body)
		clicks = append(clicks, onClick)
	}
	var mu sync.Mutex // the dialog runs on another goroutine
	var asked, answers []string
	choice := 1 // 仅本次
	b.ask = func(title, body string, buttons []string, cancel int) int {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, title+"|"+strings.Join(buttons, ","))
		return choice
	}
	answer := func(a string) {
		mu.Lock()
		answers = append(answers, a)
		mu.Unlock()
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(answers)
	}
	b.AskAgentConsent(AgentConsent{Client: "mcp:claude", Agent: "claude", Kind: "write", Summary: `SetMode("global")`}, answer)
	if b.Pending() != 1 || len(notified) != 1 {
		t.Fatalf("pending %d, notified %v", b.Pending(), notified)
	}
	items := b.extraItems()
	if len(items) != 1 || !strings.Contains(items[0].Label, "claude") {
		t.Fatalf("tray items %v", items)
	}
	items[0].Click(items[0], nil) // runs the dialog in a goroutine
	waitUntil(t, func() bool { return b.Pending() == 0 })
	waitUntil(t, func() bool { return count() == 1 })
	mu.Lock()
	defer mu.Unlock()
	if answers[0] != "once" || !strings.Contains(asked[0], "始终允许,仅本次,拒绝") {
		t.Fatalf("answers %v, asked %v", answers, asked)
	}

	// A sensitive read: two buttons; dismissing the dialog denies.
	choice = -1
	mu.Unlock()
	b.AskAgentConsent(AgentConsent{Client: "ctl", Kind: "sensitive"}, answer)
	b.decide(b.consents[0])
	mu.Lock()
	if answers[1] != "deny" || !strings.Contains(asked[1], "允许,拒绝") {
		t.Fatalf("answers %v, asked %v", answers, asked)
	}

	// Withdrawn: no answer, the item goes.
	mu.Unlock()
	withdraw := b.AskAgentConsent(AgentConsent{Client: "ctl", Kind: "danger"}, answer)
	withdraw()
	mu.Lock()
	if b.Pending() != 0 || len(answers) != 2 {
		t.Fatalf("withdraw: pending %d answers %v", b.Pending(), answers)
	}
}

// The service process turns System notices into notifications and asks
// before quitting while the system proxy is on.
func TestBackgroundNoticesAndQuit(t *testing.T) {
	f := fake.NewInstant()
	var opened []string
	b := NewBackground(f, BackgroundOptions{ShowWindow: func(arg string) { opened = append(opened, arg) }})
	var notified []func()
	b.notify = func(_, _ string, onClick func()) { notified = append(notified, onClick) }
	b.SystemNotice(app.Notice{Kind: "toast", Title: "x"}) // not System: nothing
	b.SystemNotice(app.Notice{Kind: "core-crash", Title: "内核退出", System: true})
	if len(notified) != 1 {
		t.Fatalf("notified %d", len(notified))
	}
	notified[0]()
	if len(opened) != 1 || opened[0] != OpenLogs {
		t.Fatalf("click opened %v", opened)
	}
	b.ask = func(string, string, []string, int) int { return 1 } // 取消
	if !f.Status().SystemProxy || b.ConfirmQuit() {
		t.Fatal("quit not confirmed while the system proxy is on")
	}
	_ = f.SetSystemProxy(false)
	if !b.ConfirmQuit() {
		t.Fatal("quit asked without the system proxy")
	}
	// TUN without the helper opens the window to ask there.
	if !f.TUNHelperInstalled() {
		b.setTUN(true)
		if opened[len(opened)-1] != OpenTUN {
			t.Fatalf("opened %v", opened)
		}
	}
	// Shortcuts are remembered before the app runs.
	if err := b.SetShortcut(model.ShortcutTUN, "CmdOrCtrl+Alt+T"); err != nil || b.keys.get(model.ShortcutTUN) != "CmdOrCtrl+Alt+T" {
		t.Fatalf("shortcut: %v", err)
	}
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}
