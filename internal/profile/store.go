package profile

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lanflow/internal/model"
)

// File names under Store.Dir (see the package comment for the layout).
const (
	indexFile       = "profiles.json"
	customRulesFile = "custom-rules.json"
	profilesDir     = "profiles"
	overridesDir    = "overrides"
	overrideGlobal  = "global"
)

// indexEntry is model.Subscription without runtime flags; Current is derived
// from fileIndex.Current.
type indexEntry struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Source          string    `json:"source"`
	URL             string    `json:"url,omitempty"`
	FilePath        string    `json:"filePath,omitempty"`
	Upload          int64     `json:"upload,omitempty"`
	Download        int64     `json:"download,omitempty"`
	Total           int64     `json:"total,omitempty"`
	Expire          time.Time `json:"expire,omitempty"`
	UpdatedAt       time.Time `json:"updatedAt,omitempty"`
	LastError       string    `json:"lastError,omitempty"`
	AutoUpdateHours int       `json:"autoUpdateHours,omitempty"`
	UserAgent       string    `json:"userAgent,omitempty"`
	ViaProxy        bool      `json:"viaProxy,omitempty"`
	HomePage        string    `json:"homePage,omitempty"`
	TimeoutSec      int       `json:"timeoutSec,omitempty"`
	Overrides       []string  `json:"overrides,omitempty"`
}

// fileIndex is the on-disk form of profiles.json.
type fileIndex struct {
	Current       string       `json:"current"`
	Subscriptions []indexEntry `json:"subscriptions"`
}

func toEntry(s model.Subscription) indexEntry {
	return indexEntry{
		ID: s.ID, Name: s.Name, Source: s.Source, URL: s.URL, FilePath: s.FilePath,
		Upload: s.Upload, Download: s.Download, Total: s.Total, Expire: s.Expire,
		UpdatedAt: s.UpdatedAt, LastError: s.LastError, AutoUpdateHours: s.AutoUpdateHours,
		UserAgent: s.UserAgent, ViaProxy: s.ViaProxy, HomePage: s.HomePage, TimeoutSec: s.TimeoutSec,
		Overrides: append([]string(nil), s.Overrides...),
	}
}

func (e indexEntry) toModel(current string) model.Subscription {
	return model.Subscription{
		ID: e.ID, Name: e.Name, Source: e.Source, URL: e.URL, FilePath: e.FilePath,
		Current: e.ID == current, Upload: e.Upload, Download: e.Download, Total: e.Total,
		Expire: e.Expire, UpdatedAt: e.UpdatedAt, LastError: e.LastError,
		AutoUpdateHours: e.AutoUpdateHours, UserAgent: e.UserAgent, ViaProxy: e.ViaProxy,
		HomePage: e.HomePage, TimeoutSec: e.TimeoutSec, Overrides: append([]string(nil), e.Overrides...),
	}
}

// store is the implementation behind Store. The index and custom rules are
// kept in memory and written through; content and override files are read
// from disk on demand. mu guards both the memory and the files.
type store struct {
	dir   string
	mu    sync.RWMutex
	index fileIndex
	rules []model.CustomRule
	ovs   []model.Override // the ordered override list (overrides.go)
}

func openStore(dir string) (*store, error) {
	if dir == "" {
		return nil, errors.New("配置目录为空")
	}
	for _, d := range []string{dir, filepath.Join(dir, profilesDir), filepath.Join(dir, overridesDir)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, fmt.Errorf("无法创建配置目录 %s：%w", d, err)
		}
	}
	s := &store{dir: dir}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *store) load() error {
	data, err := os.ReadFile(filepath.Join(s.dir, indexFile))
	switch {
	case err == nil:
		if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, &s.index); err != nil {
				return fmt.Errorf("订阅索引 %s 已损坏：%w", indexFile, err)
			}
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return fmt.Errorf("无法读取订阅索引：%w", err)
	}
	if s.index.Subscriptions == nil {
		s.index.Subscriptions = []indexEntry{}
	}
	// A dangling current pointer is cleared rather than reported.
	if s.index.Current != "" && s.find(s.index.Current) < 0 {
		s.index.Current = ""
	}

	data, err = os.ReadFile(filepath.Join(s.dir, customRulesFile))
	switch {
	case err == nil:
		if len(strings.TrimSpace(string(data))) > 0 {
			if err := json.Unmarshal(data, &s.rules); err != nil {
				return fmt.Errorf("自定义规则 %s 已损坏：%w", customRulesFile, err)
			}
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return fmt.Errorf("无法读取自定义规则：%w", err)
	}
	if s.rules == nil {
		s.rules = []model.CustomRule{}
	}
	return s.loadOverrides()
}

// find returns the index position of id, -1 if absent. Caller holds mu.
func (s *store) find(id string) int {
	for i, e := range s.index.Subscriptions {
		if e.ID == id {
			return i
		}
	}
	return -1
}

// newID returns a short random hex ID not present in taken.
func newID(taken func(string) bool) string {
	for {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic("profile: crypto/rand failed: " + err.Error())
		}
		id := hex.EncodeToString(b[:])
		if !taken(id) {
			return id
		}
	}
}

// validID accepts IDs we generated (lowercase hex) and rejects anything
// that could escape the directory.
func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// atomicWrite writes data to path via a temp file in the same directory and
// renames it into place. The file ends up 0600.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("无法写入 %s：%w", filepath.Base(path), err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("无法设置 %s 的权限：%w", filepath.Base(path), err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("无法写入 %s：%w", filepath.Base(path), err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("无法写入 %s：%w", filepath.Base(path), err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("无法写入 %s：%w", filepath.Base(path), err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("无法保存 %s：%w", filepath.Base(path), err)
	}
	return nil
}

func (s *store) saveIndex() error {
	data, err := json.MarshalIndent(s.index, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.dir, indexFile), append(data, '\n'))
}

func (s *store) saveRules() error {
	data, err := json.MarshalIndent(s.rules, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(s.dir, customRulesFile), append(data, '\n'))
}

func (s *store) contentPath(id string) string {
	return filepath.Join(s.dir, profilesDir, id+".yaml")
}

func (s *store) overridePath(key string) string {
	return filepath.Join(s.dir, overridesDir, key+".yaml")
}

func (s *store) list() []model.Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Subscription, len(s.index.Subscriptions))
	for i, e := range s.index.Subscriptions {
		out[i] = e.toModel(s.index.Current)
	}
	return out
}

func (s *store) get(id string) (model.Subscription, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i := s.find(id)
	if i < 0 {
		return model.Subscription{}, false
	}
	return s.index.Subscriptions[i].toModel(s.index.Current), true
}

func (s *store) current() (model.Subscription, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.index.Current == "" {
		return model.Subscription{}, false
	}
	i := s.find(s.index.Current)
	if i < 0 {
		return model.Subscription{}, false
	}
	return s.index.Subscriptions[i].toModel(s.index.Current), true
}

func (s *store) setCurrent(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != "" && s.find(id) < 0 {
		return fmt.Errorf("订阅不存在（%s），可能已被删除", id)
	}
	old := s.index.Current
	s.index.Current = id
	if err := s.saveIndex(); err != nil {
		s.index.Current = old
		return err
	}
	return nil
}

func (s *store) put(sub model.Subscription) (model.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub.ID == "" {
		sub.ID = newID(func(id string) bool { return s.find(id) >= 0 })
	} else if !validID(sub.ID) {
		return sub, fmt.Errorf("订阅 ID 无效：%q", sub.ID)
	}
	entry := toEntry(sub)
	backup := append([]indexEntry(nil), s.index.Subscriptions...)
	if i := s.find(sub.ID); i >= 0 {
		s.index.Subscriptions[i] = entry
	} else {
		s.index.Subscriptions = append(s.index.Subscriptions, entry)
	}
	if err := s.saveIndex(); err != nil {
		s.index.Subscriptions = backup
		return sub, err
	}
	out := entry.toModel(s.index.Current)
	out.Updating = sub.Updating
	return out, nil
}

func (s *store) delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.find(id)
	if i < 0 {
		return fmt.Errorf("订阅不存在（%s），可能已被删除", id)
	}
	backup := append([]indexEntry(nil), s.index.Subscriptions...)
	oldCurrent := s.index.Current
	s.index.Subscriptions = append(s.index.Subscriptions[:i:i], s.index.Subscriptions[i+1:]...)
	if s.index.Current == id {
		s.index.Current = ""
	}
	if err := s.saveIndex(); err != nil {
		s.index.Subscriptions = backup
		s.index.Current = oldCurrent
		return err
	}
	// The index is authoritative; leftover files are harmless, so errors
	// removing them are ignored.
	_ = os.Remove(s.contentPath(id))
	_ = os.Remove(s.overridePath(id))
	// The subscription's own (legacy) override entry goes with it.
	if i := s.findOverride(id); i >= 0 {
		s.ovs = append(s.ovs[:i:i], s.ovs[i+1:]...)
		_ = s.saveOverrides()
	}
	return nil
}

func (s *store) content(id string) ([]byte, error) {
	if !validID(id) {
		return nil, fmt.Errorf("订阅 ID 无效：%q", id)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, err := os.ReadFile(s.contentPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("订阅内容尚未下载，请先更新订阅")
		}
		return nil, fmt.Errorf("无法读取订阅内容：%w", err)
	}
	return data, nil
}

func (s *store) setContent(id string, data []byte) error {
	if !validID(id) {
		return fmt.Errorf("订阅 ID 无效：%q", id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return atomicWrite(s.contentPath(id), data)
}

func validOverrideKey(key string) bool {
	return key == overrideGlobal || validID(key)
}

// override and setOverride are the legacy single-text accessors, now views
// of the override list (overrides.go).
func (s *store) override(key string) string         { return s.legacyOverride(key) }
func (s *store) setOverride(key, text string) error { return s.setLegacyOverride(key, text) }

func (s *store) customRules() []model.CustomRule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.CustomRule{}, s.rules...)
}

func (s *store) setCustomRules(r []model.CustomRule) ([]model.CustomRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.CustomRule, len(r))
	copy(out, r)
	taken := map[string]bool{}
	for _, c := range out {
		if c.ID != "" {
			taken[c.ID] = true
		}
	}
	for i := range out {
		if out[i].ID == "" {
			out[i].ID = newID(func(id string) bool { return taken[id] })
			taken[out[i].ID] = true
		}
	}
	backup := s.rules
	s.rules = out
	if err := s.saveRules(); err != nil {
		s.rules = backup
		return nil, err
	}
	return append([]model.CustomRule{}, out...), nil
}
