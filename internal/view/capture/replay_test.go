package capture

import (
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
)

func waitFor(t *testing.T, tt *ui.Tester, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		tt.Frame()
		if cond() {
			tt.Frame() // draw what the condition waited for
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out; texts %q", tt.Texts())
}

// 编辑并重新发送: the editor fills from the record (pseudo headers out,
// real credentials in), sends, and shows the answer.
func TestReplayEditor(t *testing.T) {
	api := fake.NewInstant()
	arg := ""
	e, _ := testEnv(api, &arg)
	p := New(e)
	tt := ui.NewTester(p.View, 1300, 900)
	p.openReplay(3, false)
	waitFor(t, tt, func() bool { p.rp.mu.Lock(); defer p.rp.mu.Unlock(); return !p.rp.loading })
	p.rp.mu.Lock()
	method, url, hdrs, body := p.rp.method, p.rp.url, p.rp.headers, p.rp.body
	p.rp.mu.Unlock()
	if method != "POST" || !strings.HasPrefix(url, "https://") || strings.Contains(hdrs, ":authority") || !strings.Contains(body, "hunter2hunter2") {
		t.Fatalf("filled: %s %s\n%s\n%s", method, url, hdrs, body)
	}
	if !tt.HasText("重新发送请求") {
		t.Fatalf("editor not shown: %q", tt.Texts())
	}
	if err := tt.Click("发送"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("200 OK") })
	p.rp.mu.Lock()
	curl := p.rp.curl()
	p.rp.mu.Unlock()
	if !strings.HasPrefix(curl, "curl -X 'POST' 'https://") || !strings.Contains(curl, "--data-raw") {
		t.Fatalf("curl: %s", curl)
	}
	if out := os.Getenv("REPLAY_PREVIEW"); out != "" {
		tt.SetScale(2)
		tt.Advance(time.Second)
		tt.Frame()
		w, _ := os.Create(out)
		png.Encode(w, tt.Image())
		w.Close()
	}
}

func TestParseHeaderLines(t *testing.T) {
	got := parseHeaderLines("A: 1\r\n\n  B :two: parts \nnovalue\n:x")
	want := []model.KV{{Key: "A", Value: "1"}, {Key: "B", Value: "two: parts"}, {Key: "novalue", Value: ""}}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%v", got)
		}
	}
}

// Pausing recording drops the connection rows and the topic.
func TestPauseRecording(t *testing.T) {
	api := fake.NewInstant()
	arg := ""
	e, _ := testEnv(api, &arg)
	p := New(e)
	tt := ui.NewTester(p.View, 1300, 800)
	if err := tt.Click("暂停记录"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !api.CaptureStatus().Settings.NoRecord || !tt.HasText("继续记录") {
		t.Fatalf("not paused: %q", tt.Texts())
	}
	if p.Wants("conns") {
		t.Fatal("still wants connections")
	}
	if err := tt.Click("继续记录"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if api.CaptureStatus().Settings.NoRecord || !p.Wants("conns") {
		t.Fatal("not resumed")
	}
}
