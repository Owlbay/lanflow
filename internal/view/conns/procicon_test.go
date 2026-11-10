package conns

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
)

// noIcons is the fake backend without process icons.
type noIcons struct{ *fake.Fake }

func (noIcons) ProcessIcon(string) []byte { return nil }

// TestProcessIconsInList renders the 进程 column with the fake's sample
// icons and without any.
func TestProcessIconsInList(t *testing.T) {
	f := fake.NewInstant()
	e, _ := testEnv(f)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 700)
	if !tt.HasText("Safari") {
		t.Fatalf("process column missing: %q", tt.Texts())
	}
	if p.icons.Source("/Applications/Safari.app") == nil {
		t.Fatal("the list did not load Safari's icon")
	}

	e2, _ := testEnv(f)
	e2.API = noIcons{f}
	p2 := New(e2)
	tt2 := ui.NewTester(p2.View, 1000, 700)
	if !tt2.HasText("Safari") {
		t.Fatalf("process column missing without icons: %q", tt2.Texts())
	}
	if p2.icons.Source("/Applications/Safari.app") != nil {
		t.Fatal("an icon out of nothing")
	}
}

// TestProcessIconsInTopology paints the 进程 column's icons in the
// topology view, with and without icons to show.
func TestProcessIconsInTopology(t *testing.T) {
	for _, icons := range []bool{true, false} {
		f := fake.NewInstant()
		e, _ := testEnv(f)
		if !icons {
			e.API = noIcons{f}
		}
		p := New(e)
		tt := ui.NewTester(p.View, 1000, 700)
		if err := tt.Click("拓扑"); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		g := p.topo.graph
		if g == nil || len(p.topo.layout.Boxes) != len(g.Nodes) {
			t.Fatalf("topology not painted (icons=%v)", icons)
		}
		var procs int
		for _, n := range g.Nodes {
			if n.Column == ColProcess && !n.Other && n.Path != "" {
				procs++
			}
		}
		if procs == 0 {
			t.Fatalf("no process node carries its path (icons=%v): %+v", icons, g.Nodes)
		}
		have := p.icons.Source("/Applications/Safari.app") != nil
		if have != icons {
			t.Fatalf("Safari's icon loaded = %v while icons = %v", have, icons)
		}
	}
}
