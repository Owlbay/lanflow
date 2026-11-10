package split

import (
	"sync"
	"time"

	"lanflow/internal/app"
)

// Notices is the service's app.API with the notice queue shared between
// the service process and the window process. app.API.TakeNotices hands a
// notice to one caller only; the service process must see the ones that
// become system notifications (Notice.System) whether or not a window is
// open, and the window the rest when it next opens. So Notices takes them
// from the service (Pull, on every TopicNotice), passes each new one to
// OnNew and keeps it for the window's TakeNotices.
type Notices struct {
	app.API
	// OnNew is called once per notice as it is taken from the service.
	OnNew func(app.Notice)

	mu    sync.Mutex
	queue []kept
	now   func() time.Time
}

type kept struct {
	n  app.Notice
	at time.Time
}

// Limits of the queue kept while no window takes it.
const (
	keepNotices = 20
	keepToasts  = 2 * time.Minute // a passing message is not news later
)

// NewNotices wraps api; call Pull from an OnChange listener.
func NewNotices(api app.API) *Notices { return &Notices{API: api, now: time.Now} }

// Pull takes the service's new notices into the queue.
func (n *Notices) Pull() {
	fresh := n.API.TakeNotices()
	if len(fresh) == 0 {
		return
	}
	now := n.now()
	n.mu.Lock()
	for _, x := range fresh {
		n.queue = append(n.queue, kept{x, now})
	}
	if over := len(n.queue) - keepNotices; over > 0 {
		n.queue = append([]kept(nil), n.queue[over:]...)
	}
	n.mu.Unlock()
	if n.OnNew != nil {
		for _, x := range fresh {
			n.OnNew(x)
		}
	}
}

// TakeNotices implements app.API for the window: what was kept, except
// toasts that are no longer current.
func (n *Notices) TakeNotices() []app.Notice {
	n.Pull()
	now := n.now()
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []app.Notice
	for _, k := range n.queue {
		if k.n.Kind == "toast" && now.Sub(k.at) > keepToasts {
			continue
		}
		out = append(out, k.n)
	}
	n.queue = nil
	return out
}

// Unwrap is the service itself, for its optional interfaces.
func (n *Notices) Unwrap() app.API { return n.API }

// CacheUsage and CleanCaches pass the optional cache API through.
func (n *Notices) CacheUsage() []app.CacheItem {
	if c, ok := n.API.(interface{ CacheUsage() []app.CacheItem }); ok {
		return c.CacheUsage()
	}
	return nil
}

func (n *Notices) CleanCaches() (int64, error) {
	if c, ok := n.API.(interface{ CleanCaches() (int64, error) }); ok {
		return c.CleanCaches()
	}
	return 0, nil
}
