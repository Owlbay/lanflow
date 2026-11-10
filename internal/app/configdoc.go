package app

import (
	"errors"
	"fmt"
	"strings"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/syncfile"
)

// lanflow.yaml (package syncfile) from and to the service's state. The
// same document serves configuration sync (configsync.go), where links
// are keychain references, and the export / WebDAV backup, where they
// are written out when the user asks for them.

// docLinks says how buildDoc writes subscription links.
type docLinks int

const (
	linksNone     docLinks = iota // no subscriptions at all (export without links)
	linksPlain                    // the links themselves (export with links, backup)
	linksKeychain                 // keychain:sub-<id> (sync)
)

// localState is the configuration a document is built from.
type localState struct {
	set   model.Settings
	ovs   []model.Override
	rules []model.CustomRule
	subs  []model.Subscription
}

// snapshotLocal reads the configuration (the store from disk; call
// without mu).
func (s *Service) snapshotLocal() localState {
	s.mu.Lock()
	set := copySettings(s.state.settings)
	s.mu.Unlock()
	return localState{set: set, ovs: s.d.store.Overrides(), rules: s.d.store.CustomRules(), subs: s.d.store.List()}
}

// buildDoc turns st into lanflow.yaml's document.
func buildDoc(st localState, links docLinks) syncfile.Doc {
	doc := syncfile.Doc{Format: syncfile.Format, Settings: copySettings(st.set)}
	synced := map[string]bool{} // subscriptions in the document
	if links != linksNone {
		for _, sub := range st.subs {
			if sub.Source == urlSubscriptionK {
				synced[sub.ID] = true
			}
		}
	}
	local := map[string]bool{} // subscriptions on this machine only
	for _, sub := range st.subs {
		if !synced[sub.ID] {
			local[sub.ID] = true
		}
	}
	listed := map[string]bool{}
	for _, o := range st.ovs {
		// A subscription's legacy entry travels with its subscription.
		if local[o.ID] {
			continue
		}
		listed[o.ID] = true
		e := syncfile.Override{ID: o.ID, Name: o.Name, Enabled: o.Enabled, Global: o.Global, Text: o.Text}
		if o.Source == model.OverrideURL {
			e.URL = o.URL
		}
		doc.Overrides = append(doc.Overrides, e)
	}
	for _, r := range st.rules {
		doc.CustomRules = append(doc.CustomRules, syncfile.Rule{Type: r.Type, Payload: r.Payload, Target: r.Target})
	}
	for _, sub := range st.subs {
		if !synced[sub.ID] {
			continue
		}
		e := syncfile.Subscription{
			ID: sub.ID, Name: sub.Name, UserAgent: sub.UserAgent, ViaProxy: sub.ViaProxy,
			AutoUpdateHours: sub.AutoUpdateHours, TimeoutSec: sub.TimeoutSec,
		}
		for _, id := range sub.Overrides {
			if listed[id] {
				e.Overrides = append(e.Overrides, id)
			}
		}
		switch {
		case links == linksKeychain && sub.URL != "":
			e.URL = syncfile.SubscriptionRef(sub.ID)
		case links == linksPlain:
			e.URL = sub.URL
		}
		doc.Subscriptions = append(doc.Subscriptions, e)
	}
	return doc
}

// docToExport turns a lanflow.yaml read for import into the export the
// import path applies; resolve gives a keychain reference's value
// ("" when this machine has none). Links it cannot resolve are listed.
func docToExport(doc syncfile.Doc, resolve func(ref string) string) (model.SettingsExport, []string) {
	set := doc.Settings
	ex := model.SettingsExport{App: exportApp, Format: exportFormat, Settings: &set, Overrides: &model.Overrides{PerSub: map[string]string{}}}
	for _, o := range doc.Overrides {
		ex.Overrides.List = append(ex.Overrides.List, overrideFromDoc(o))
	}
	for _, r := range doc.CustomRules {
		ex.CustomRules = append(ex.CustomRules, model.CustomRule{Type: r.Type, Payload: r.Payload, Target: r.Target})
	}
	var missing []string
	for _, sub := range doc.Subscriptions {
		u := sub.URL
		if strings.HasPrefix(u, syncfile.KeychainPrefix) {
			u = resolve(u)
		}
		if u == "" {
			missing = append(missing, sub.Name)
			continue
		}
		ex.Subscriptions = append(ex.Subscriptions, model.ExportedSubscription{
			Name: sub.Name, URL: u, UserAgent: sub.UserAgent, ViaProxy: sub.ViaProxy, AutoUpdateHours: sub.AutoUpdateHours,
		})
	}
	return ex, missing
}

func overrideFromDoc(o syncfile.Override) model.Override {
	out := model.Override{ID: o.ID, Name: o.Name, Enabled: o.Enabled, Global: o.Global, Source: model.OverrideLocal, Text: o.Text}
	if o.URL != "" {
		out.Source, out.URL = model.OverrideURL, o.URL
	}
	return out
}

// secretStore returns the keychain the service uses (nil = none).
func (s *Service) secretStore() SecretStore {
	if s.cs.secrets != nil {
		return s.cs.secrets
	}
	return Secrets
}

// resolveRef reads a keychain reference; "" when the keychain does not
// have it (or there is no keychain).
func (s *Service) resolveRef(ref string) string {
	key, ok := syncfile.KeychainKey(ref)
	st := s.secretStore()
	if !ok || st == nil {
		return ""
	}
	v, err := st.Get(key)
	if err != nil {
		if !errors.Is(err, ErrSecretNotFound) {
			s.alog.Warnf("config sync: keychain read %s: %v", key, err)
		}
		return ""
	}
	return string(v)
}

// encodeExport is the export file: lanflow.yaml with the links written
// out when includeSubs, without any subscription otherwise.
func (s *Service) encodeExport(includeSubs bool) []byte {
	links := linksNone
	if includeSubs {
		links = linksPlain
	}
	return syncfile.Encode(buildDoc(s.snapshotLocal(), links))
}

// parseImport reads an import file: lanflow.yaml, or the JSON export of
// earlier versions. fromDoc is true for lanflow.yaml.
func (s *Service) parseImport(data []byte) (ex model.SettingsExport, notes []string, err error) {
	if !syncfile.Sniff(data) {
		ex, err = parseExport(data)
		return ex, nil, err
	}
	doc, err := syncfile.Decode(data, defaultSettings())
	if err != nil {
		return ex, nil, fmt.Errorf(i18n.T("文件无效：%v"), err)
	}
	ex, missing := docToExport(doc, s.resolveRef)
	for _, name := range missing {
		notes = append(notes, fmt.Sprintf(i18n.T("订阅「%s」（文件中只有钥匙串引用，本机钥匙串里没有它的链接）"), name))
	}
	for i, r := range ex.CustomRules {
		if _, err := validateCustomRule(r); err != nil {
			return ex, nil, fmt.Errorf(i18n.T("第 %d 条自定义规则无效：%v"), i+1, err)
		}
	}
	return ex, notes, nil
}
