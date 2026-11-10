package app

// ring is a fixed-capacity buffer keeping the newest n items: push is O(1)
// and never reallocates (unlike append-then-reslice, which keeps copying
// and pins ever larger backing arrays).
type ring[T any] struct {
	buf  []T
	head int // index of the oldest item
	n    int // items held
}

func newRing[T any](capacity int) ring[T] { return ring[T]{buf: make([]T, capacity)} }

// push adds v, dropping the oldest item when full.
func (r *ring[T]) push(v T) {
	if len(r.buf) == 0 {
		return
	}
	if r.n < len(r.buf) {
		r.buf[(r.head+r.n)%len(r.buf)] = v
		r.n++
		return
	}
	r.buf[r.head] = v
	r.head = (r.head + 1) % len(r.buf)
}

func (r *ring[T]) len() int { return r.n }

// at returns the i-th oldest item (0 = oldest).
func (r *ring[T]) at(i int) T { return r.buf[(r.head+i)%len(r.buf)] }

// oldestFirst returns a copy of the items, oldest first.
func (r *ring[T]) oldestFirst() []T {
	out := make([]T, r.n)
	for i := range out {
		out[i] = r.at(i)
	}
	return out
}

// newestFirst returns a copy of the items, newest first.
func (r *ring[T]) newestFirst() []T {
	out := make([]T, r.n)
	for i := range out {
		out[i] = r.at(r.n - 1 - i)
	}
	return out
}

// reset empties the ring, releasing the items for the collector.
func (r *ring[T]) reset() {
	clear(r.buf)
	r.head, r.n = 0, 0
}
