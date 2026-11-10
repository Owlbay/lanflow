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

// SaveUIState records u and writes settings.json.
func (s *Service) SaveUIState(u model.UIState) error {
	s.mu.Lock()
	s.state.settings.UI = copyUIState(u)
	set := s.state.settings
	s.mu.Unlock()
	return saveSettings(s.opt.DataDir, set)
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
