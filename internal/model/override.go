package model

import "time"

// Override sources (Override.Source).
const (
	OverrideLocal = "local" // text written or imported from a file
	OverrideURL   = "url"   // text downloaded from URL, refreshed by API.UpdateOverrideEntry
)

// Override is one entry of the ordered YAML override list (docs/design.md
// §6.9). Composition applies, in list order, the enabled overrides a
// subscription selects (Subscription.Overrides) and every enabled global
// one, after the subscription and before Lanflow's own settings.
//
// Two IDs are special, for configurations written by older versions: the
// entry "global" is the former single global override, and an entry whose
// ID is a subscription's ID is that subscription's former override.
type Override struct {
	ID        string
	Name      string
	Enabled   bool
	Global    bool   // applies to every subscription
	Source    string // OverrideLocal or OverrideURL
	URL       string // for Source == OverrideURL
	Text      string // the YAML
	UpdatedAt time.Time
}
