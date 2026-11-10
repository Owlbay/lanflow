package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"lanflow/internal/i18n"
)

// 「清理缓存」 (settings › 数据): what Lanflow keeps on disk that can be
// thrown away and comes back by itself, with its size, and the action
// that removes it. The UI reaches these through an optional interface on
// the API, so the contract (api.go) is unchanged.

// CacheItem is one kind of cache: its name for the user, where it lives
// and how much it holds.
type CacheItem struct {
	Name  string // Chinese, translated (i18n)
	Path  string
	Bytes int64
	Files int
}

// cacheKinds lists the caches: a name and the files of the kind.
func (s *Service) cacheKinds() []struct {
	name  string
	paths []string
	dir   bool
} {
	logs := []string{s.logFile + ".1", filepath.Join(s.opt.LogDir, "lanflow.log.1"), s.tunLogFile() + ".1"}
	return []struct {
		name  string
		paths []string
		dir   bool
	}{
		{i18n.T("图标"), []string{filepath.Join(s.opt.CacheDir, iconsDir), filepath.Join(s.opt.CacheDir, procIconsDir)}, true},
		{i18n.T("内核版本列表"), []string{s.releaseCachePath("stable"), s.releaseCachePath("alpha")}, false},
		{i18n.T("内核缓存"), []string{filepath.Join(s.workDir, "cache.db")}, false},
		{i18n.T("旧日志"), logs, false},
	}
}

// CacheUsage reports every cache and its size on disk. It reads the
// directory listings and nothing else; the UI refreshes it now and then.
func (s *Service) CacheUsage() []CacheItem {
	var out []CacheItem
	for _, k := range s.cacheKinds() {
		it := CacheItem{Name: k.name, Path: k.paths[0]}
		for _, p := range k.paths {
			if k.dir {
				entries, err := os.ReadDir(p)
				if err != nil {
					continue
				}
				for _, e := range entries {
					if info, err := e.Info(); err == nil && !e.IsDir() {
						it.Bytes += info.Size()
						it.Files++
					}
				}
				continue
			}
			if info, err := os.Stat(p); err == nil && !info.IsDir() {
				it.Bytes += info.Size()
				it.Files++
			}
		}
		out = append(out, it)
	}
	return out
}

// CleanCaches removes the caches and returns how many bytes they held.
// The core's own cache.db is left alone while the core runs (it holds
// the file open; the fake-ip and selection it stores are the core's to
// manage) and the in-memory icon and release caches are dropped with the
// files, so the next frames fetch what they show again.
func (s *Service) CleanCaches() (freed int64, err error) {
	defer func() { s.logOp(fmt.Sprintf("clean caches (%d bytes)", freed), err) }()
	running := s.running()
	var first error
	remove := func(p string) {
		info, serr := os.Stat(p)
		if serr != nil || info.IsDir() {
			return
		}
		if rerr := os.Remove(p); rerr != nil {
			if first == nil {
				first = rerr
			}
			return
		}
		freed += info.Size()
	}
	for _, k := range s.cacheKinds() {
		for _, p := range k.paths {
			if strings.HasSuffix(p, "cache.db") && running {
				continue
			}
			if !k.dir {
				remove(p)
				continue
			}
			entries, rerr := os.ReadDir(p)
			if rerr != nil {
				continue
			}
			for _, e := range entries {
				if !e.IsDir() {
					remove(filepath.Join(p, e.Name()))
				}
			}
		}
	}
	s.mu.Lock()
	s.state.icons = nil
	s.state.procIcons = nil
	s.mu.Unlock()
	s.cores.mu.Lock()
	s.cores.releases = nil
	s.cores.mu.Unlock()
	if first != nil {
		return freed, fmt.Errorf(i18n.T("清理缓存失败：%v"), first)
	}
	s.em.emit(TopicProxies, TopicProcIcons, TopicExits)
	return freed, nil
}
