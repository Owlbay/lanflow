package stats

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
)

// noIcons is the fake backend without process icons.
type noIcons struct{ *fake.Fake }

func (noIcons) ProcessIcon(string) []byte { return nil }

// TestProcessIcons renders the 进程 dimension with the fake's sample icons
// and without any; the other dimensions never ask for icons.
func TestProcessIcons(t *testing.T) {
	for _, icons := range []bool{true, false} {
		f := fake.NewInstant()
		e, _ := testEnv(f)
		if !icons {
			e.API = noIcons{f}
		}
		p := New(e)
		p.dim = indexOf(dimLabels, "进程")
		tt := ui.NewTester(p.View, 1000, 700)
		tt.Frame()
		buckets := f.TrafficStats(model.StatsToday).Buckets[model.StatsProcess]
		if len(buckets) == 0 || !tt.HasText(buckets[0].Key) {
			t.Fatalf("process rows missing (icons=%v): %q", icons, tt.Texts())
		}
		var loaded int
		for _, b := range buckets {
			if p.icons.Source(b.Detail) != nil {
				loaded++
			}
		}
		if icons && loaded == 0 {
			t.Fatalf("no process icon loaded from %+v", buckets)
		}
		if !icons && loaded != 0 {
			t.Fatalf("%d icons out of nothing", loaded)
		}
	}
}
