package env

import (
	"sync"
	"testing"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// TestFmtBytesOneDecimal: sizes keep one decimal at every magnitude and a
// space between number and unit (docs/design.md §10).
func TestFmtBytesOneDecimal(t *testing.T) {
	cases := map[int64]string{
		0:                     "0 B",
		512:                   "512 B",
		1023:                  "1023 B",
		1024:                  "1.0 KB",
		12_698:                "12.4 KB",
		1_289_748:             "1.2 MB",
		1 << 30:               "1.0 GB",
		34_896_609_280:        "32.5 GB",
		1_099_511_627_776 + 1: "1.0 TB",
		1_649_267_441_664:     "1.5 TB",
		2_200_000_000:         "2.0 GB",
		1_320_702_607:         "1.2 GB",
	}
	for in, want := range cases {
		if got := FmtBytes(in); got != want {
			t.Errorf("FmtBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestFmtSpeedAndGB(t *testing.T) {
	if got := FmtSpeed(1_289_748); got != "1.2 MB/s" {
		t.Errorf("FmtSpeed = %q", got)
	}
	if got := FmtSpeed(0); got != "0 B/s" {
		t.Errorf("FmtSpeed(0) = %q", got)
	}
	if got := FmtGB(34_896_609_280); got != "32.5" {
		t.Errorf("FmtGB = %q", got)
	}
}

func TestFmtDelayAndKind(t *testing.T) {
	for _, c := range []struct {
		ms      int
		testing bool
		text    string
		kind    string
	}{
		{86, false, "86 ms", "green"}, {300, false, "300 ms", "green"}, {301, false, "301 ms", "orange"},
		{800, false, "800 ms", "orange"}, {801, false, "801 ms", "red"}, {-1, false, "超时", "red"},
		{0, false, "—", "gray"}, {120, true, "…", "green"},
	} {
		if got := FmtDelay(c.ms, c.testing); got != c.text {
			t.Errorf("FmtDelay(%d,%v) = %q, want %q", c.ms, c.testing, got, c.text)
		}
		if got := DelayKind(c.ms); got != c.kind {
			t.Errorf("DelayKind(%d) = %q, want %q", c.ms, got, c.kind)
		}
	}
}

func TestFmtDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{5 * time.Second: "0:05", 133 * time.Second: "2:13", time.Hour + 2*time.Minute + 3*time.Second: "1:02:03"} {
		if got := FmtDuration(d); got != want {
			t.Errorf("FmtDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

// fakeUI is a UI state store for TestUIStateSaver.
type fakeUI struct {
	app.API
	mu    sync.Mutex
	u     model.UIState
	saves int
}

func (f *fakeUI) UIState() model.UIState { f.mu.Lock(); defer f.mu.Unlock(); return f.u }
func (f *fakeUI) UpdateUIState(fn func(*model.UIState)) error {
	f.mu.Lock()
	u := f.u
	f.mu.Unlock()
	fn(&u)
	return f.SaveUIState(u)
}
func (f *fakeUI) SaveUIState(u model.UIState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.u, f.saves = u, f.saves+1
	return nil
}

// TestUIStateSaver: changes in a burst are one write, the latest change
// of a key wins, and changes of different keys all land.
func TestUIStateSaver(t *testing.T) {
	f := &fakeUI{}
	s := NewUIStateSaver(f, 30*time.Millisecond)
	s.Save("logs", func(u *model.UIState) { u.LogLevel = "info" })
	s.Save("proxies", func(u *model.UIState) { u.ProxyGrid = true })
	s.Save("logs", func(u *model.UIState) { u.LogLevel = "error" })
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.mu.Lock()
		n := f.saves
		f.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	s.Save("proxies", func(u *model.UIState) { u.OpenGroups = map[string]bool{"a": true} })
	s.Close()
	u := f.UIState()
	if f.saves != 2 || u.LogLevel != "error" || !u.ProxyGrid || !u.OpenGroups["a"] {
		t.Fatalf("saves %d, state %+v", f.saves, u)
	}
}

// TestFmtDateByLanguage: dates follow the language shown.
func TestFmtDateByLanguage(t *testing.T) {
	t.Cleanup(func() { i18n.SetLang(i18n.ZH) })
	d := time.Date(2026, 11, 1, 10, 21, 0, 0, time.Local)
	if got := FmtDate(d); got != "2026-11-01" {
		t.Errorf("zh %q", got)
	}
	i18n.SetLang(i18n.EN)
	if got, gotT := FmtDate(d), FmtDateTime(d); got != "Nov 1, 2026" || gotT != "Nov 1, 2026 10:21" {
		t.Errorf("en %q %q", got, gotT)
	}
}
