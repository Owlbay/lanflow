package capture

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

func testEnv(api *fake.Fake, arg *string) (*env.Env, *[]string) {
	var toasts []string
	e := &env.Env{API: api, Kit: env.Basic(), Mac: true,
		Navigate: func(env.Page, string) {},
		Arg: func(env.Page) string {
			a := *arg
			*arg = ""
			return a
		},
		Toast: func(m string, bad bool) { toasts = append(toasts, m) },
		Run: func(job func() error, ok string) {
			if err := job(); err != nil {
				toasts = append(toasts, err.Error())
			} else if ok != "" {
				toasts = append(toasts, ok)
			}
		},
		Confirm: func(title, body, okLabel string, danger bool, onOK func()) { onOK() },
		Redraw:  func() {},
	}
	return e, &toasts
}

func TestListDetailAndMasking(t *testing.T) {
	api := fake.NewInstant()
	arg := ""
	e, _ := testEnv(api, &arg)
	p := New(e)
	tt := ui.NewTester(p.View, 1300, 800)
	if !tt.HasText("api.github.com") || !tt.HasText("pinned.example.com") {
		t.Fatalf("rows missing: %q", tt.Texts())
	}
	// select the OAuth request (form body with a password)
	p.sel = 3
	tt.Frame()
	waitDetail(t, p, tt)
	if err := tt.Click("请求体"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	body := strings.Join(tt.Texts(), "\n")
	if strings.Contains(body, "hunter2hunter2") || strings.Contains(body, "abcdef123456") || !strings.Contains(body, "grant_type=password") {
		t.Fatalf("form body not masked:\n%s", body)
	}
	if err := tt.Click("请求头"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if strings.Contains(strings.Join(tt.Texts(), "\n"), "ghp_abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Fatal("authorization header shown unmasked")
	}
	p.reveal = true
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), "\n"), "ghp_abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Fatal("reveal did not show the value")
	}
	// URL query values masked in the list
	if strings.Contains(strings.Join(tt.Texts(), "\n"), "access_token=abcdef0123456789") {
		t.Fatal("token in the path column")
	}
}

func waitDetail(t *testing.T, p *Page, tt *ui.Tester) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		p.det.Lock()
		ok := !p.det.loading && p.det.d.Flow.ID == p.sel
		p.det.Unlock()
		if ok {
			tt.Frame()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("detail did not load")
}

func TestSwitchAndAddHost(t *testing.T) {
	api := fake.NewInstant()
	arg := "add-host:new.example.org"
	e, toasts := testEnv(api, &arg)
	p := New(e)
	tt := ui.NewTester(p.View, 1300, 800)
	st := api.CaptureStatus()
	found := false
	for _, d := range st.Settings.Domains {
		found = found || d == "new.example.org"
	}
	if !found || !st.Enabled {
		t.Fatalf("add-host did not add and turn on: %+v %v", st.Settings.Domains, *toasts)
	}
	if !tt.HasText(StatusText(st)) {
		t.Fatalf("status text missing: %q", tt.Texts())
	}
	if err := tt.Click("关闭解密"); err != nil {
		t.Fatal(err)
	}
	if api.CaptureStatus().Enabled {
		t.Fatal("not turned off")
	}
}

func TestStatusText(t *testing.T) {
	st := model.CaptureStatus{Enabled: true, Domains: 2, Apps: 1, Until: time.Now().Add(90 * time.Second)}
	if got := StatusText(st); !strings.HasPrefix(got, "解密中 · 2 个主机 · 1 个程序 · 剩余 01:") {
		t.Fatal(got)
	}
}

func TestEnglish(t *testing.T) {
	i18n.SetLang("en")
	defer i18n.SetLang("zh")
	api := fake.NewInstant()
	arg := ""
	e, _ := testEnv(api, &arg)
	p := New(e)
	tt := ui.NewTester(p.View, 1300, 800)
	p.sel = 1
	waitDetail(t, p, tt)
	for _, tab := range []int{0, 1, 2, 3, 4} {
		p.tab = tab
		tt.Frame()
		for _, s := range tt.Texts() {
			if hasCJK(s) && !strings.Contains(s, "证书固定：") {
				t.Errorf("tab %d: Chinese in English UI: %q", tab, s)
			}
		}
	}
	p.editing = true
	tt.Frame()
	for _, s := range tt.Texts() {
		if hasCJK(s) {
			t.Errorf("editor: Chinese in English UI: %q", s)
		}
	}
}

func hasCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff || r >= 0x3000 && r <= 0x303f || r >= 0xff00 && r <= 0xffef {
			return true
		}
	}
	return false
}

// Every connection is listed, decrypted or not (as Surge's 最近请求); an
// undecrypted one offers decrypting its host, which adds it to the
// selection.
func TestConnectionsListedAndDecryptFromRow(t *testing.T) {
	api := fake.NewInstant()
	api.ClearCaptureFlows()
	arg := ""
	e, _ := testEnv(api, &arg)
	p := New(e)
	tt := ui.NewTester(p.View, 1300, 800)
	if !tt.HasText("slack.com") || !tt.HasText("未解密") {
		t.Fatalf("connections not listed: %q", tt.Texts())
	}
	var slack model.CaptureFlow
	for _, f := range p.rows() {
		if f.Host == "slack.com" {
			slack = f
		}
	}
	if slack.ID >= 0 || slack.Port != 443 || slack.Process != "Slack" {
		t.Fatalf("slack row = %+v", slack)
	}
	p.sel = slack.ID
	tt.Frame()
	if err := tt.Click("解密此主机"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if d := api.CaptureStatus().Settings.Domains; len(d) == 0 || d[len(d)-1] != "slack.com" {
		t.Fatalf("selection = %v", d)
	}
	if !strings.Contains(strings.Join(tt.Texts(), "\n"), "已在解密选择中") || tt.HasText("解密此主机") {
		t.Fatalf("detail does not show the host as chosen: %q", tt.Texts())
	}
	// 只看已解密 hides the connections
	p.onlyDec = true
	tt.Frame()
	if tt.HasText("slack.com") {
		t.Fatal("connection shown with 只看已解密")
	}
}

// Sorted by time, connections and decrypted records interleave by start
// (connections have negative IDs and once all sank to the bottom).
func TestTimeSortInterleavesConnections(t *testing.T) {
	api := fake.NewInstant()
	arg := ""
	e, _ := testEnv(api, &arg)
	p := New(e)
	rows := p.rows()
	for i := 1; i < len(rows); i++ {
		if rows[i].Start.After(rows[i-1].Start) {
			t.Fatalf("row %d (%s %s) newer than row %d (%s %s)", i, rows[i].Host, rows[i].Start, i-1, rows[i-1].Host, rows[i-1].Start)
		}
	}
	if fmtDur(850) != "850 ms" || fmtDur(2049) != "2.0 s" || fmtDur(75000) != "75 s" {
		t.Fatal("fmtDur")
	}
}
