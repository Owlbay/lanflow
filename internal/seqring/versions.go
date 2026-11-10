package seqring

// Versions follows a list whose items change in place (the active
// connections: their byte counters move every second) and answers which
// items were added, changed or removed since a cursor.
type Versions[T any] struct {
	id    func(T) string
	equal func(a, b T) bool

	seq   uint64
	floor uint64 // removals at or before floor are forgotten: older cursors get the whole list
	ver   map[string]uint64
	prev  map[string]T
	gone  []removal
}

type removal struct {
	id  string
	seq uint64
}

// keepRemovals is how many updates removals are remembered for; a client
// that has not read for longer gets the whole list.
const keepRemovals = 600

// NewVersions follows items by id, comparing them with equal.
func NewVersions[T any](id func(T) string, equal func(a, b T) bool) *Versions[T] {
	return &Versions[T]{id: id, equal: equal, ver: map[string]uint64{}, prev: map[string]T{}}
}

// Update records the current list. Not safe for concurrent use.
func (v *Versions[T]) Update(items []T) {
	v.seq++
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		id := v.id(it)
		seen[id] = true
		if old, ok := v.prev[id]; !ok || !v.equal(old, it) {
			v.ver[id] = v.seq
			v.prev[id] = it
		}
	}
	for id := range v.prev {
		if !seen[id] {
			delete(v.prev, id)
			delete(v.ver, id)
			v.gone = append(v.gone, removal{id, v.seq})
		}
	}
	for len(v.gone) > 0 && v.seq-v.gone[0].seq > keepRemovals {
		v.floor = v.gone[0].seq
		v.gone = v.gone[1:]
	}
	if len(v.gone) == 0 && cap(v.gone) > 1024 {
		v.gone = nil
	}
}

// Seq is the cursor of the current list.
func (v *Versions[T]) Seq() uint64 { return v.seq }

// Delta is the answer of Since.
type Delta[T any] struct {
	Cursor  uint64
	Full    bool     // Changed is the whole list: replace the copy held
	Changed []T      // new or changed items, in list order
	Removed []string // ids of items gone since
}

// Since returns what changed in items (the list last passed to Update)
// after since; since 0 returns the whole list.
func (v *Versions[T]) Since(items []T, since uint64) Delta[T] {
	d := Delta[T]{Cursor: v.seq}
	if since == 0 || since < v.floor || since > v.seq {
		d.Full = true
		d.Changed = append([]T(nil), items...)
		return d
	}
	for _, it := range items {
		if v.ver[v.id(it)] > since {
			d.Changed = append(d.Changed, it)
		}
	}
	for _, g := range v.gone {
		if g.seq > since {
			d.Removed = append(d.Removed, g.id)
		}
	}
	return d
}
