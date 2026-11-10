package settings

import (
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/view/env"
)

// 缓存 row of 设置 › 数据: the sizes of what Lanflow caches on disk (proxy
// group and application icons, the core's release lists and cache.db,
// rotated logs) and 「清理缓存」. A backend offers it through an optional
// interface (app.Service does); without it the row is not shown.

// cacheAPI is the optional service side of the row.
type cacheAPI interface {
	CacheUsage() []app.CacheItem
	CleanCaches() (freed int64, err error)
}

// cacheRefresh is how old the sizes shown may get before they are read
// again (the listing is cheap, but not per frame).
const cacheRefresh = 5 * time.Second

type cacheState struct {
	mu    sync.Mutex
	items []app.CacheItem
	at    time.Time
	busy  bool
}

// usage returns the sizes, re-read when stale.
func (cs *cacheState) usage(api cacheAPI, now time.Time) []app.CacheItem {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.items == nil || now.Sub(cs.at) > cacheRefresh {
		cs.items = api.CacheUsage()
		cs.at = now
	}
	return cs.items
}

// cacheRow draws the row.
func (p *Page) cacheRow(c *ui.Context) {
	api, ok := p.e.API.(cacheAPI)
	if !ok {
		return
	}
	e := p.e
	items := p.caches.usage(api, time.Now())
	p.caches.mu.Lock()
	busy := p.caches.busy
	p.caches.mu.Unlock()
	e.Kit.Row(c, i18n.T("缓存"), cacheSummary(items), func() {
		b := ui.Button(c, i18n.T("清理缓存")).Disabled(busy)
		if b.Clicked() {
			p.caches.mu.Lock()
			p.caches.busy = true
			p.caches.mu.Unlock()
			e.Run(func() error {
				freed, err := api.CleanCaches()
				p.caches.mu.Lock()
				p.caches.busy, p.caches.items = false, nil
				p.caches.mu.Unlock()
				if err != nil {
					return err
				}
				e.Toast(i18n.Tf("已清理 %s", env.FmtBytes(freed)), false)
				return nil
			}, "")
		}
	})
}

// cacheSummary is the row's description: each cache with a size, then
// the total, e.g. 「图标 1.2 MB · 内核缓存 128.0 KB · 共 1.3 MB」.
func cacheSummary(items []app.CacheItem) string {
	var parts []string
	var total int64
	for _, it := range items {
		total += it.Bytes
		if it.Bytes > 0 {
			parts = append(parts, it.Name+" "+env.FmtBytes(it.Bytes))
		}
	}
	if total == 0 {
		return i18n.T("没有可清理的缓存")
	}
	parts = append(parts, i18n.Tf("共 %s", env.FmtBytes(total)))
	return strings.Join(parts, " · ")
}
