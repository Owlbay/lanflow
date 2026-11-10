package app

import "lanflow/internal/model"

// UI state (docs/design.md §5.2 展开状态: remember each group's expanded
// state) lives in Settings.UI and is saved with settings.json, but through
// its own methods so a click on a group header never goes near the core.

// UIState returns the remembered presentation state.
func (s *Service) UIState() model.UIState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyUIState(s.state.settings.UI)
}

// SaveUIState records u and writes settings.json. It replaces the whole
// state: two pages that read, change and save their own field race with
// each other; UpdateUIState does not.
func (s *Service) SaveUIState(u model.UIState) error {
	return s.UpdateUIState(func(cur *model.UIState) { *cur = u })
}

// UpdateUIState changes the remembered presentation state in place and
// writes settings.json: fn gets the current state (a private copy) under
// the settings lock, so concurrent updates of different fields (one page's
// open groups, another's log level) never lose each other. fn must not
// block or call back into the service.
func (s *Service) UpdateUIState(fn func(*model.UIState)) error {
	return s.updateSettings(func(set *model.Settings) {
		u := copyUIState(set.UI)
		fn(&u)
		set.UI = copyUIState(u)
	})
}

func copyUIState(u model.UIState) model.UIState {
	out := u
	if u.OpenGroups != nil {
		out.OpenGroups = make(map[string]bool, len(u.OpenGroups))
		for k, v := range u.OpenGroups {
			out.OpenGroups[k] = v
		}
	}
	return out
}
