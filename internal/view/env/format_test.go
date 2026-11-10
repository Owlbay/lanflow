package env

import (
	"testing"
	"time"
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
