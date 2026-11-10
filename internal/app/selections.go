package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"

	"lanflow/internal/model"
)

// selectionsFile keeps the node picked in every select group, per
// subscription: {"<subscription id>": {"<group>": "<member>"}}.
//
// The core's own cache.db (profile.store-selected) remembers selections
// too, but only by group name and only while that file survives: two
// subscriptions with the same group names overwrite each other, cleaning
// the core cache forgets them, and a core that cannot lock cache.db at
// start (the previous one still exiting) neither restores nor saves any.
// Lanflow's own record is applied after every start and reload, on top of
// whatever the core restored.
const selectionsFile = "selections.json"

func (s *Service) selectionsPath() string { return filepath.Join(s.opt.DataDir, selectionsFile) }

// loadSelectionsLocked reads the record once; selMu must be held.
func (s *Service) loadSelectionsLocked() map[string]map[string]string {
	if s.selections != nil {
		return s.selections
	}
	s.selections = map[string]map[string]string{}
	if b, err := os.ReadFile(s.selectionsPath()); err == nil {
		_ = json.Unmarshal(b, &s.selections)
	}
	return s.selections
}

// selectionKey is the current subscription's id ("" when there is none).
func (s *Service) selectionKey() string {
	if cur, ok := s.d.store.Current(); ok {
		return cur.ID
	}
	return ""
}

// rememberSelection records a selection the user made.
func (s *Service) rememberSelection(group, name string) {
	if group == probeGroup {
		return
	}
	key := s.selectionKey()
	s.selMu.Lock()
	defer s.selMu.Unlock()
	all := s.loadSelectionsLocked()
	if all[key] == nil {
		all[key] = map[string]string{}
	}
	if all[key][group] == name {
		return
	}
	all[key][group] = name
	// Drop the record of subscriptions that no longer exist.
	subs := s.d.store.List()
	for id := range all {
		if id != "" && id != key && !slices.ContainsFunc(subs, func(sub model.Subscription) bool { return sub.ID == id }) {
			delete(all, id)
		}
	}
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	if err := writeFileAtomic(s.selectionsPath(), data, 0o600); err != nil {
		s.alog.Warnf("save selections: %v", err)
	}
}

// restoreSelections re-selects the remembered members of the current
// subscription's select groups where the core shows something else.
// Groups or members that are gone are skipped.
func (s *Service) restoreSelections() {
	key := s.selectionKey()
	s.selMu.Lock()
	want := make(map[string]string, len(s.loadSelectionsLocked()[key]))
	for g, n := range s.selections[key] {
		want[g] = n
	}
	s.selMu.Unlock()
	if len(want) == 0 {
		return
	}
	changed := 0
	for _, g := range s.Proxies().Groups {
		name, ok := want[g.Name]
		if !ok || g.Now == name || !slices.Contains(g.All, name) {
			continue
		}
		if g.Type != model.GroupSelect && g.Name != "GLOBAL" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), ctrlTimeout)
		err := s.d.ctrl.SelectProxy(ctx, g.Name, name)
		cancel()
		if err != nil {
			s.alog.Warnf("restore selection %s -> %s: %v", g.Name, name, err)
			continue
		}
		changed++
	}
	if changed > 0 {
		s.alog.Infof("restored %d group selection(s)", changed)
		s.refreshProxies()
	}
}
