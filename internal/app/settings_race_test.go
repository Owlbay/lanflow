package app

import (
	"fmt"
	"sync"
	"testing"

	"lanflow/internal/model"
)

// Concurrent read-modify-write updates of different UI fields and of other
// settings must all survive, in memory and in settings.json: the last
// write always carries the latest snapshot.
func TestConcurrentSettingsUpdatesKeepEveryChange(t *testing.T) {
	h := newHarness(t, nil)
	const n = 24
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if err := h.s.UpdateUIState(func(u *model.UIState) {
				if u.OpenGroups == nil {
					u.OpenGroups = map[string]bool{}
				}
				u.OpenGroups[fmt.Sprintf("g%d", i)] = true
			}); err != nil {
				t.Error(err)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_ = h.s.UpdateUIState(func(u *model.UIState) { u.LogLevel = "debug" })
			} else {
				_ = h.s.SetMainGroup("main")
			}
		}(i)
	}
	wg.Wait()
	check := func(where string, set model.Settings) {
		t.Helper()
		if len(set.UI.OpenGroups) != n || set.UI.LogLevel != "debug" || set.MainGroup != "main" {
			t.Fatalf("%s lost updates: %d groups, level %q, main %q", where, len(set.UI.OpenGroups), set.UI.LogLevel, set.MainGroup)
		}
	}
	check("memory", h.s.Settings())
	disk, err := loadSettings(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	check("settings.json", disk)
}

// SaveUIState of a stale copy must not be torn by a concurrent writer: the
// file always parses.
func TestConcurrentSaveUIStateFileStaysValid(t *testing.T) {
	h := newHarness(t, nil)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = h.s.SaveUIState(model.UIState{ProxyGrid: i%2 == 0, OpenGroups: map[string]bool{"a": true}})
			if _, err := loadSettings(h.dir); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
}
