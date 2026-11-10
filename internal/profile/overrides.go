package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"lanflow/internal/model"
	"lanflow/internal/netx"
)

// The ordered override list (model.Override) lives in overrides.json under
// Store.Dir. The former single texts (overrides/global.yaml and
// overrides/<id>.yaml) are migrated into entries with the IDs "global" and
// <id> the first time a store without overrides.json is opened; the
// subscription's entry is selected for it, so the composed config is the
// same as before.

const overridesFile = "overrides.json"

// Layer is one override text applied by Compose, with the name the
// runtime-config view attributes its lines to.
type Layer struct {
	Name string
	Text string
}

// Layers returns the overrides that apply to sub, in list order: the
// enabled global ones and the enabled ones sub selects. The legacy entries
// keep their former layer names (全局覆写, 订阅覆写); the others are named
// after the entry.
func Layers(list []model.Override, sub model.Subscription) []Layer {
	selected := map[string]bool{}
	for _, id := range sub.Overrides {
		selected[id] = true
	}
	var out []Layer
	for _, o := range list {
		if !o.Enabled || (!o.Global && !selected[o.ID]) {
			continue
		}
		if strings.TrimSpace(o.Text) == "" {
			continue
		}
		name := o.Name
		switch {
		case o.ID == overrideGlobal:
			name = layerGlobal
		case sub.ID != "" && o.ID == sub.ID:
			name = layerSubOverride
		case name == "":
			name = o.ID
		}
		out = append(out, Layer{Name: name, Text: o.Text})
	}
	return out
}

// ---- Store API ----

// Overrides returns the override list in order (copies).
func (s *Store) Overrides() []model.Override { return s.s.overrides() }

// GetOverride returns the override with that ID.
func (s *Store) GetOverride(id string) (model.Override, bool) { return s.s.getOverride(id) }

// PutOverride inserts (empty ID: appended with a new ID) or updates an
// override and returns it.
func (s *Store) PutOverride(o model.Override) (model.Override, error) { return s.s.putOverride(o) }

// DeleteOverride removes an override and drops it from every
// subscription's selection.
func (s *Store) DeleteOverride(id string) error { return s.s.deleteOverride(id) }

// MoveOverride moves an override by delta positions (-1 = up).
func (s *Store) MoveOverride(id string, delta int) error { return s.s.moveOverride(id, delta) }

// ---- store internals (mu held by the callers below as noted) ----

func (s *store) overridesPath() string { return filepath.Join(s.dir, overridesFile) }

// loadOverrides reads overrides.json, migrating the former single texts
// when it does not exist yet. Called by load with no lock held (the store
// is not shared yet).
func (s *store) loadOverrides() error {
	data, err := os.ReadFile(s.overridesPath())
	switch {
	case err == nil:
		if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, &s.ovs); err != nil {
				return fmt.Errorf("覆写列表 %s 已损坏：%w", overridesFile, err)
			}
		}
		if s.ovs == nil {
			s.ovs = []model.Override{}
		}
		return nil
	case errors.Is(err, os.ErrNotExist):
		return s.migrateOverrides()
	default:
		return fmt.Errorf("无法读取覆写列表：%w", err)
	}
}

// migrateOverrides turns overrides/<id>.yaml and overrides/global.yaml into
// list entries: the subscription ones first, the global one last, so the
// former order (订阅覆写 → 全局覆写) is kept.
func (s *store) migrateOverrides() error {
	s.ovs = []model.Override{}
	read := func(key string) (string, bool) {
		data, err := os.ReadFile(s.overridePath(key))
		if err != nil || strings.TrimSpace(string(data)) == "" {
			return "", false
		}
		return string(data), true
	}
	var old []string
	now := time.Now()
	for i := range s.index.Subscriptions {
		e := &s.index.Subscriptions[i]
		text, ok := read(e.ID)
		if !ok {
			continue
		}
		s.ovs = append(s.ovs, model.Override{ID: e.ID, Name: e.Name + " 覆写", Enabled: true, Source: model.OverrideLocal, Text: text, UpdatedAt: now})
		if !hasID(e.Overrides, e.ID) {
			e.Overrides = append(e.Overrides, e.ID)
		}
		old = append(old, e.ID)
	}
	if text, ok := read(overrideGlobal); ok {
		s.ovs = append(s.ovs, model.Override{ID: overrideGlobal, Name: "全局覆写", Enabled: true, Global: true, Source: model.OverrideLocal, Text: text, UpdatedAt: now})
		old = append(old, overrideGlobal)
	}
	if err := s.saveOverrides(); err != nil {
		return err
	}
	if len(old) > 0 {
		if err := s.saveIndex(); err != nil {
			return err
		}
		for _, key := range old {
			_ = os.Remove(s.overridePath(key))
		}
	}
	return nil
}

func (s *store) saveOverrides() error {
	data, err := json.MarshalIndent(s.ovs, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(s.overridesPath(), append(data, '\n'))
}

func hasID(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// findOverride returns the position of id in s.ovs, -1 if absent. Caller holds mu.
func (s *store) findOverride(id string) int {
	for i, o := range s.ovs {
		if o.ID == id {
			return i
		}
	}
	return -1
}

func (s *store) overrides() []model.Override {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.Override{}, s.ovs...)
}

func (s *store) getOverride(id string) (model.Override, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if i := s.findOverride(id); i >= 0 {
		return s.ovs[i], true
	}
	return model.Override{}, false
}

func (s *store) putOverride(o model.Override) (model.Override, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o.ID == "" {
		o.ID = newID(func(id string) bool { return s.findOverride(id) >= 0 || s.find(id) >= 0 })
	} else if !validOverrideKey(o.ID) {
		return o, fmt.Errorf("覆写 ID 无效：%q", o.ID)
	}
	if o.Source != model.OverrideURL {
		o.Source = model.OverrideLocal
	}
	if o.ID == overrideGlobal {
		o.Global = true
	}
	backup := append([]model.Override(nil), s.ovs...)
	if i := s.findOverride(o.ID); i >= 0 {
		s.ovs[i] = o
	} else {
		s.ovs = append(s.ovs, o)
	}
	if err := s.saveOverrides(); err != nil {
		s.ovs = backup
		return o, err
	}
	return o, nil
}

func (s *store) deleteOverride(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findOverride(id)
	if i < 0 {
		return fmt.Errorf("覆写不存在（%s），可能已被删除", id)
	}
	backup := append([]model.Override(nil), s.ovs...)
	s.ovs = append(s.ovs[:i:i], s.ovs[i+1:]...)
	if err := s.saveOverrides(); err != nil {
		s.ovs = backup
		return err
	}
	changed := false
	for k := range s.index.Subscriptions {
		e := &s.index.Subscriptions[k]
		if hasID(e.Overrides, id) {
			e.Overrides = without(e.Overrides, id)
			changed = true
		}
	}
	if changed {
		return s.saveIndex()
	}
	return nil
}

func without(xs []string, x string) []string {
	out := xs[:0:0]
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

func (s *store) moveOverride(id string, delta int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.findOverride(id)
	if i < 0 {
		return fmt.Errorf("覆写不存在（%s），可能已被删除", id)
	}
	j := i + delta
	if j < 0 {
		j = 0
	}
	if j >= len(s.ovs) {
		j = len(s.ovs) - 1
	}
	if j == i {
		return nil
	}
	backup := append([]model.Override(nil), s.ovs...)
	o := s.ovs[i]
	s.ovs = append(s.ovs[:i:i], s.ovs[i+1:]...)
	s.ovs = append(s.ovs[:j], append([]model.Override{o}, s.ovs[j:]...)...)
	if err := s.saveOverrides(); err != nil {
		s.ovs = backup
		return err
	}
	return nil
}

// legacyOverride is Store.Override: the text of the entry whose ID is key.
func (s *store) legacyOverride(key string) string {
	if !validOverrideKey(key) {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if i := s.findOverride(key); i >= 0 {
		return s.ovs[i].Text
	}
	return ""
}

// setLegacyOverride is Store.SetOverride: it writes the text of the entry
// whose ID is key, creating it ("global" as a global entry, a subscription
// ID as an entry that subscription selects) and deleting it when text is
// empty, as the single texts behaved.
func (s *store) setLegacyOverride(key, text string) error {
	if !validOverrideKey(key) {
		return fmt.Errorf("覆写键无效：%q（应为 global 或订阅 ID）", key)
	}
	if text == "" {
		if _, ok := s.getOverride(key); !ok {
			return nil
		}
		return s.deleteOverride(key)
	}
	s.mu.Lock()
	var o model.Override
	if i := s.findOverride(key); i >= 0 {
		o = s.ovs[i]
	} else {
		o = model.Override{ID: key, Enabled: true, Source: model.OverrideLocal}
		if key == overrideGlobal {
			o.Name, o.Global = "全局覆写", true
		} else if k := s.find(key); k >= 0 {
			o.Name = s.index.Subscriptions[k].Name + " 覆写"
		} else {
			o.Name = key
		}
	}
	s.mu.Unlock()
	o.Text = text
	o.UpdatedAt = time.Now()
	if _, err := s.putOverride(o); err != nil {
		return err
	}
	// A subscription's own entry is selected by it.
	s.mu.Lock()
	defer s.mu.Unlock()
	if k := s.find(key); k >= 0 && !hasID(s.index.Subscriptions[k].Overrides, key) {
		s.index.Subscriptions[k].Overrides = append(s.index.Subscriptions[k].Overrides, key)
		return s.saveIndex()
	}
	return nil
}

// ---- download ----

// maxOverrideBody is the largest override text accepted from a URL.
const maxOverrideBody = 4 << 20

// FetchOverride downloads an override's YAML from url with client (nil =
// a direct client) and checks it is a key/value mapping. Errors are
// user-facing Chinese like Fetch's.
func FetchOverride(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	if client == nil {
		client = netx.Direct(30 * time.Second)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("覆写链接无效：请填写以 http:// 或 https:// 开头的完整地址")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", errors.New("覆写链接无效：" + sanitizeErr(err, u))
	}
	req.Header.Set("User-Agent", netx.UserAgent)
	req.Header.Set("Accept", "*/*")
	resp, err := client.Do(req)
	if err != nil {
		return "", transportError(err, u)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", statusError(resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOverrideBody+1))
	if err != nil {
		return "", transportError(err, u)
	}
	if len(body) > maxOverrideBody {
		return "", errors.New("下载失败：覆写内容超过 4 MB，不像是覆写文件，请检查链接")
	}
	text := string(body)
	if _, err := parseOverride(text); err != nil {
		return "", fmt.Errorf("下载的内容不是 YAML 覆写：%s", err.Error())
	}
	return text, nil
}
