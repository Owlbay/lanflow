package app

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Process icons: the application icon shown beside a process name in the
// 连接 list, the topology's 进程 column and the 统计 page. ProcessIcon maps
// a connection's ProcessPath to the thing whose icon is shown (iconSubject:
// the enclosing .app bundle on macOS, the executable elsewhere) and draws
// it through deps.fileIcon (MyGo's Shell.FileIcon) once, in the
// background, into a disk cache under CacheDir/procicons keyed by the
// subject's path and modification time. TopicProcIcons fires when an icon
// lands.

const (
	procIconsDir      = "procicons"
	procIconPx        = 32       // 16 DIPs at a 2× display; the UI scales it down
	maxProcIconBytes  = 64 << 10 // one icon
	maxProcIconsCache = 4 << 20  // the whole cache directory
	maxProcIconsInMem = 512      // decoded icons kept in memory
)

// procIconCache holds the icons loaded and those being loaded, by subject.
type procIconCache struct {
	mu      sync.Mutex
	data    map[string][]byte // subject → PNG
	failed  map[string]bool   // subject → has no icon
	pending map[string]bool   // subject → a load runs
}

func (s *Service) procIconsLocked() *procIconCache {
	if s.state.procIcons == nil {
		s.state.procIcons = &procIconCache{data: map[string][]byte{}, failed: map[string]bool{}, pending: map[string]bool{}}
	}
	return s.state.procIcons
}

// ProcessIcon implements API.ProcessIcon.
func (s *Service) ProcessIcon(path string) []byte {
	subject := iconSubject(strings.TrimSpace(path))
	if subject == "" || s.d.fileIcon == nil {
		return nil
	}
	s.mu.Lock()
	c := s.procIconsLocked()
	s.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if d, ok := c.data[subject]; ok {
		return d
	}
	if c.failed[subject] || c.pending[subject] {
		return nil
	}
	c.pending[subject] = true
	s.goBackground(func() { s.loadProcIcon(c, subject) })
	return nil
}

// loadProcIcon fills the cache for subject from disk or the platform, and
// emits TopicProcIcons when an icon was found.
func (s *Service) loadProcIcon(c *procIconCache, subject string) {
	var data []byte
	var err error
	switch cached := s.procIconPath(subject); {
	case cached == "":
		err = &iconError{"not found"}
	case readProcIcon(cached, &data):
	default:
		data, err = s.d.fileIcon(subject, procIconPx)
		if err == nil && (len(data) == 0 || len(data) > maxProcIconBytes) {
			err = &iconError{"empty or over 64 KB"}
		}
		if err == nil {
			storeProcIcon(cached, data)
		}
	}
	c.mu.Lock()
	delete(c.pending, subject)
	if err != nil {
		c.failed[subject] = true
	} else {
		if len(c.data) >= maxProcIconsInMem {
			c.data = map[string][]byte{}
		}
		c.data[subject] = data
	}
	c.mu.Unlock()
	if err != nil {
		s.alog.Infof("process icon %s: %v", subject, err)
		return
	}
	s.em.emit(TopicProcIcons)
}

// procIconPath is where subject's icon is cached: CacheDir/procicons/<key>.png,
// or "" when the subject cannot be stat'ed (and so has no icon either).
func (s *Service) procIconPath(subject string) string {
	info, err := os.Stat(subject)
	if err != nil {
		return ""
	}
	return filepath.Join(s.opt.CacheDir, procIconsDir, procIconKey(subject, info.ModTime().UnixNano())+".png")
}

// procIconKey names the cache file of a subject at a modification time.
func procIconKey(subject string, mtime int64) string {
	sum := sha256.Sum256([]byte(subject + "\x00" + strconv.FormatInt(mtime, 10)))
	return hex.EncodeToString(sum[:16])
}

// readProcIcon reads the cached icon at path into *data; false when there
// is none worth using.
func readProcIcon(path string, data *[]byte) bool {
	d, err := os.ReadFile(path)
	if err != nil || len(d) == 0 || len(d) > maxProcIconBytes {
		return false
	}
	*data = d
	return true
}

// storeProcIcon writes an icon to its cache path, keeping the directory
// under maxProcIconsCache.
func storeProcIcon(path string, data []byte) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	trimIconCache(dir, maxProcIconsCache-int64(len(data)))
	_ = writeFileAtomic(path, data, 0o600)
}

// appBundleOf returns the outermost .app bundle enclosing path (the
// application the executable belongs to, so that a browser's helper
// processes show the browser's icon), "" when path is not inside one.
// It works on the path text alone.
func appBundleOf(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	sep := "/"
	if strings.Contains(path, `\`) && !strings.Contains(path, "/") {
		sep = `\`
	}
	parts := strings.Split(path, sep)
	for i, p := range parts {
		if len(p) > len(".app") && strings.EqualFold(filepath.Ext(p), ".app") {
			return strings.Join(parts[:i+1], sep)
		}
	}
	return ""
}
