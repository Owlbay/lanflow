package app

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Proxy-group icons (the `icon` field of proxy-groups, docs/design.md
// §5.2). The controller does not report it, so the runtime config is read
// at commit time (groupIconsOf) and Proxies() fills ProxyGroup.Icon from
// it. IconData turns a reference into image bytes: data: URLs and inline
// SVG at once, http(s) URLs through a direct client into a disk cache
// under CacheDir/icons, fetched once in the background.

const (
	iconsDir      = "icons"
	maxIconBytes  = 512 << 10 // one icon
	maxIconsCache = 16 << 20  // the whole cache directory
	iconTimeout   = 15 * time.Second
)

// groupIconsOf reads proxy-groups[].icon from a composed config.
func groupIconsOf(yamlText []byte) map[string]string {
	var doc struct {
		Groups []struct {
			Name string `yaml:"name"`
			Icon string `yaml:"icon"`
		} `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal(yamlText, &doc); err != nil {
		return nil
	}
	out := map[string]string{}
	for _, g := range doc.Groups {
		if icon := strings.TrimSpace(g.Icon); icon != "" && g.Name != "" {
			out[g.Name] = icon
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Bounds of the in-memory icon cache: the icons of the configs used in one
// run are a few dozen, but a config can name any number of URLs.
const (
	maxIconsMem    = 8 << 20          // bytes of decoded icons kept in memory
	maxIconsFailed = 512              // failed references remembered
	iconRetryAfter = 30 * time.Minute // a failed URL is tried again after this
)

// iconCache holds the icons already loaded and those being fetched.
type iconCache struct {
	mu      sync.Mutex
	data    map[string][]byte    // reference → bytes
	order   []string             // data's keys, oldest first (eviction order)
	size    int                  // bytes in data
	failed  map[string]time.Time // reference → when it could not be loaded
	pending map[string]bool      // reference → a download runs
}

func (s *Service) iconsLocked() *iconCache {
	if s.state.icons == nil {
		s.state.icons = &iconCache{data: map[string][]byte{}, failed: map[string]time.Time{}, pending: map[string]bool{}}
	}
	return s.state.icons
}

// putLocked stores an icon, evicting the oldest ones beyond maxIconsMem.
func (c *iconCache) putLocked(ref string, d []byte) {
	if old, ok := c.data[ref]; ok {
		c.size -= len(old)
	} else {
		c.order = append(c.order, ref)
	}
	c.data[ref] = d
	c.size += len(d)
	for c.size > maxIconsMem && len(c.order) > 1 {
		victim := c.order[0]
		c.order = c.order[1:]
		c.size -= len(c.data[victim])
		delete(c.data, victim)
	}
}

// failLocked remembers a failed reference, forgetting the oldest beyond
// maxIconsFailed.
func (c *iconCache) failLocked(ref string, now time.Time) {
	if len(c.failed) >= maxIconsFailed {
		oldest, at := "", now
		for r, t := range c.failed {
			if t.Before(at) || oldest == "" {
				oldest, at = r, t
			}
		}
		delete(c.failed, oldest)
	}
	c.failed[ref] = now
}

// failedLocked reports a reference that failed recently; an old failure is
// forgotten so the icon is tried again.
func (c *iconCache) failedLocked(ref string, now time.Time) bool {
	t, ok := c.failed[ref]
	if ok && now.Sub(t) >= iconRetryAfter {
		delete(c.failed, ref)
		return false
	}
	return ok
}

// IconData implements API.IconData.
func (s *Service) IconData(ref string) ([]byte, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, false
	}
	s.mu.Lock()
	c := s.iconsLocked()
	s.mu.Unlock()
	c.mu.Lock()
	if d, ok := c.data[ref]; ok {
		c.mu.Unlock()
		return d, true
	}
	if c.failedLocked(ref, time.Now()) || c.pending[ref] {
		c.mu.Unlock()
		return nil, false
	}
	c.mu.Unlock()

	if d, ok := decodeInlineIcon(ref); ok {
		c.mu.Lock()
		c.putLocked(ref, d)
		c.mu.Unlock()
		return d, true
	}
	if !strings.HasPrefix(ref, "http://") && !strings.HasPrefix(ref, "https://") {
		c.mu.Lock()
		c.failLocked(ref, time.Now())
		c.mu.Unlock()
		return nil, false
	}
	if d, err := os.ReadFile(s.iconPath(ref)); err == nil && len(d) > 0 {
		c.mu.Lock()
		c.putLocked(ref, d)
		c.mu.Unlock()
		return d, true
	}
	c.mu.Lock()
	c.pending[ref] = true
	c.mu.Unlock()
	s.goBackground(func() { s.fetchIcon(c, ref) })
	return nil, false
}

// decodeInlineIcon decodes a data: URL or inline SVG reference.
func decodeInlineIcon(ref string) ([]byte, bool) {
	if strings.HasPrefix(ref, "<svg") || strings.HasPrefix(ref, "<?xml") {
		return []byte(ref), true
	}
	if !strings.HasPrefix(ref, "data:") {
		return nil, false
	}
	meta, payload, ok := strings.Cut(ref[len("data:"):], ",")
	if !ok {
		return nil, false
	}
	var d []byte
	if strings.HasSuffix(meta, ";base64") {
		var err error
		d, err = base64.StdEncoding.DecodeString(strings.TrimSpace(payload))
		if err != nil {
			d, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(payload))
		}
		if err != nil {
			return nil, false
		}
	} else {
		un, err := url.PathUnescape(payload)
		if err != nil {
			return nil, false
		}
		d = []byte(un)
	}
	if len(d) == 0 || len(d) > maxIconBytes {
		return nil, false
	}
	return d, true
}

// iconPath is where a URL icon is cached: CacheDir/icons/<sha256 of the URL>.
func (s *Service) iconPath(ref string) string {
	sum := sha256.Sum256([]byte(ref))
	return filepath.Join(s.opt.CacheDir, iconsDir, hex.EncodeToString(sum[:16]))
}

// fetchIcon downloads one icon into the cache and emits TopicProxies.
func (s *Service) fetchIcon(c *iconCache, ref string) {
	data, err := s.downloadIcon(ref)
	c.mu.Lock()
	delete(c.pending, ref)
	if err != nil {
		c.failLocked(ref, time.Now())
	} else {
		c.putLocked(ref, data)
	}
	c.mu.Unlock()
	if err != nil {
		s.alog.Infof("group icon %s: %v", ref, err)
		return
	}
	s.em.emit(TopicProxies)
}

func (s *Service) downloadIcon(ref string) ([]byte, error) {
	ctx, cancel := s.ctrlCtx(nil, iconTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Lanflow")
	resp, err := s.d.httpDirect(iconTimeout).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &iconError{"HTTP " + resp.Status}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxIconBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxIconBytes {
		return nil, &iconError{"empty or over 512 KB"}
	}
	dir := filepath.Join(s.opt.CacheDir, iconsDir)
	if err := os.MkdirAll(dir, 0o700); err == nil {
		trimIconCache(dir, maxIconsCache-int64(len(data)))
		_ = writeFileAtomic(s.iconPath(ref), data, 0o600)
	}
	return data, nil
}

type iconError struct{ msg string }

func (e *iconError) Error() string { return e.msg }

// trimIconCache removes the oldest cached icons until the directory holds
// at most limit bytes.
func trimIconCache(dir string, limit int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type file struct {
		name string
		size int64
		mod  time.Time
	}
	var files []file
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		files = append(files, file{e.Name(), info.Size(), info.ModTime()})
		total += info.Size()
	}
	for total > limit && len(files) > 0 {
		oldest := 0
		for i := range files {
			if files[i].mod.Before(files[oldest].mod) {
				oldest = i
			}
		}
		_ = os.Remove(filepath.Join(dir, files[oldest].name))
		total -= files[oldest].size
		files = append(files[:oldest], files[oldest+1:]...)
	}
}
