package logs

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/model"
)

// The level filter and the source are remembered in the UI state and
// restored by a new page.
func TestLevelAndSourcePersist(t *testing.T) {
	api := &logAPI{Fake: fake.NewInstant()}
	e, _ := testEnv(api)
	p := New(e)
	tt := ui.NewTester(p.View, 1000, 700)
	p.level = levelRank["error"]
	p.persist()
	p.source = 1
	p.persist()
	u := api.UIState()
	if u.LogLevel != "error" || u.LogSource != "app" {
		t.Fatalf("UI state = %+v", u)
	}
	// What other pages remember survives.
	u.OpenGroups = map[string]bool{"节点选择": true}
	if err := api.SaveUIState(u); err != nil {
		t.Fatal(err)
	}
	p.level = levelRank["debug"]
	p.persist()
	if got := api.UIState(); got.LogLevel != "debug" || !got.OpenGroups["节点选择"] {
		t.Fatalf("persist must keep other fields: %+v", got)
	}
	tt.Frame()

	q := New(e)
	ui.NewTester(q.View, 1000, 700)
	if q.level != levelRank["debug"] || !q.isApp() {
		t.Fatalf("restored level=%d source=%d", q.level, q.source)
	}
	_ = model.UIState{}
}
