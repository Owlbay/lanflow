// Package seqring keeps the newest items of a stream with a sequence
// number each, so that a client in another process can read only what it
// has not seen (app.API LogsSince, ClosedConnectionsSince, AppLogsSince,
// ConnectionsSince; docs/service-split.md §3 大数据).
//
// Sequence numbers start at 1 and never repeat within one Ring; a client
// keeps the Cursor of the last page and passes it back as since. A page
// says Reset when the client's copy cannot be continued (the ring was
// cleared, items after since were already dropped, or since is unknown,
// e.g. from before a restart of the service): the client then replaces
// what it holds with the page.
package seqring

// Ring is a fixed-capacity buffer keeping the newest n items: Push is
// O(1) and never reallocates. Not safe for concurrent use.
type Ring[T any] struct {
	buf   []T
	size  int    // capacity of a lazily allocated buf (NewLazy)
	head  int    // index of the oldest item
	n     int    // items held
	total uint64 // sequence number of the newest item ever pushed
	epoch uint64 // total right after the last Reset: older cursors are stale
}

// New returns a ring holding at most capacity items.
func New[T any](capacity int) Ring[T] { return Ring[T]{buf: make([]T, capacity)} }

// NewLazy is New with the buffer allocated by the first Push.
func NewLazy[T any](capacity int) Ring[T] { return Ring[T]{size: capacity} }

// Push adds v, dropping the oldest item when full.
func (r *Ring[T]) Push(v T) {
	if r.buf == nil && r.size > 0 {
		r.buf = make([]T, r.size)
	}
	if len(r.buf) == 0 {
		return
	}
	r.total++
	if r.n < len(r.buf) {
		r.buf[(r.head+r.n)%len(r.buf)] = v
		r.n++
		return
	}
	r.buf[r.head] = v
	r.head = (r.head + 1) % len(r.buf)
}

// Len is the number of items held.
func (r *Ring[T]) Len() int { return r.n }

// At returns the i-th oldest item (0 = oldest).
func (r *Ring[T]) At(i int) T { return r.buf[(r.head+i)%len(r.buf)] }

// Seq is the sequence number of the newest item pushed (0 = none yet).
func (r *Ring[T]) Seq() uint64 { return r.total }

// OldestFirst returns a copy of the items, oldest first.
func (r *Ring[T]) OldestFirst() []T {
	out := make([]T, r.n)
	for i := range out {
		out[i] = r.At(i)
	}
	return out
}

// NewestFirst returns a copy of the items, newest first.
func (r *Ring[T]) NewestFirst() []T {
	out := make([]T, r.n)
	for i := range out {
		out[i] = r.At(r.n - 1 - i)
	}
	return out
}

// Reset empties the ring, releasing the items for the collector. Cursors
// handed out before are stale afterwards.
func (r *Ring[T]) Reset() {
	clear(r.buf)
	r.head, r.n = 0, 0
	r.total++ // burn a number: a cursor equal to the old total must not look current
	r.epoch = r.total
}

// Page is one incremental read.
type Page[T any] struct {
	Items  []T    // oldest first
	Cursor uint64 // pass back as since for the next read
	Reset  bool   // replace the copy held with Items (and the pages that follow)
	More   bool   // more items follow: read again with Cursor
}

// Since returns the items pushed after since, oldest first, at most limit
// of them (limit <= 0: all). since 0 reads from the oldest item held.
func (r *Ring[T]) Since(since uint64, limit int) Page[T] {
	return pageOf(r.n, r.At, r.total, r.epoch, since, limit)
}

// SliceSince is Since for a list kept elsewhere: items oldest first, total
// the sequence number of the last item, epoch total right after the list
// was last cleared.
func SliceSince[T any](items []T, total, epoch, since uint64, limit int) Page[T] {
	return pageOf(len(items), func(i int) T { return items[i] }, total, epoch, since, limit)
}

func pageOf[T any](n int, at func(int) T, total, epoch, since uint64, limit int) Page[T] {
	first := total - uint64(n) // the items held are first+1 … total
	p := Page[T]{Cursor: total}
	start := 0 // index of the first item to return
	if since == 0 || since < epoch || since < first || since > total {
		p.Reset = true
	} else {
		start = int(since - first)
	}
	count := n - start
	if limit > 0 && count > limit {
		count, p.More = limit, true
	}
	if count <= 0 {
		return p
	}
	p.Items = make([]T, count)
	for i := range p.Items {
		p.Items[i] = at(start + i)
	}
	p.Cursor = first + uint64(start+count)
	return p
}
