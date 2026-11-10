package seqring

import (
	"slices"
	"testing"
)

// follow reads r from cursor and applies the page to have, as a client does.
func follow(r *Ring[int], have []int, cursor uint64, limit int) ([]int, uint64) {
	for {
		p := r.Since(cursor, limit)
		if p.Reset {
			have = nil
		}
		have = append(have, p.Items...)
		cursor = p.Cursor
		if !p.More {
			return have, cursor
		}
	}
}

func TestRingSince(t *testing.T) {
	r := New[int](5)
	var have []int
	var cur uint64
	have, cur = follow(&r, have, cur, 0)
	if len(have) != 0 || cur != 0 {
		t.Fatalf("empty: %v %d", have, cur)
	}
	for i := 1; i <= 3; i++ {
		r.Push(i)
	}
	have, cur = follow(&r, have, cur, 2) // two pages
	if !slices.Equal(have, []int{1, 2, 3}) {
		t.Fatalf("have %v", have)
	}
	if p := r.Since(cur, 0); p.Reset || len(p.Items) != 0 || p.Cursor != cur {
		t.Fatalf("nothing new: %+v", p)
	}
	r.Push(4)
	r.Push(5)
	have, cur = follow(&r, have, cur, 0)
	if !slices.Equal(have, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("have %v", have)
	}
	// Overflow: 6 drops 1; a client at 5 continues, one left behind
	// (cursor 0 or older than the oldest held) starts over.
	r.Push(6)
	have, cur = follow(&r, have, cur, 0)
	if !slices.Equal(have[len(have)-5:], []int{2, 3, 4, 5, 6}) {
		t.Fatalf("have %v", have)
	}
	for i := 7; i <= 20; i++ {
		r.Push(i)
	}
	if p := r.Since(cur, 0); !p.Reset || !slices.Equal(p.Items, []int{16, 17, 18, 19, 20}) {
		t.Fatalf("left behind: %+v", p)
	}
	// Reset: an old cursor (even the newest one) must reset.
	_, cur = follow(&r, nil, 0, 0)
	r.Reset()
	if p := r.Since(cur, 0); !p.Reset || len(p.Items) != 0 {
		t.Fatalf("after reset: %+v", p)
	}
	_, cur2 := follow(&r, nil, cur, 0)
	r.Push(21)
	if p := r.Since(cur2, 0); p.Reset || !slices.Equal(p.Items, []int{21}) {
		t.Fatalf("after reset, continued: %+v", p)
	}
	// A cursor from the future (a service restarted) resets.
	if p := r.Since(cur2+100, 0); !p.Reset {
		t.Fatalf("future cursor: %+v", p)
	}
}

func TestLazyRing(t *testing.T) {
	r := NewLazy[string](2)
	if r.buf != nil {
		t.Fatal("allocated early")
	}
	r.Push("a")
	r.Push("b")
	r.Push("c")
	if !slices.Equal(r.OldestFirst(), []string{"b", "c"}) || !slices.Equal(r.NewestFirst(), []string{"c", "b"}) {
		t.Fatalf("got %v", r.OldestFirst())
	}
}

func TestSliceSinceMatchesRing(t *testing.T) {
	r := New[int](4)
	for i := 1; i <= 6; i++ {
		r.Push(i)
	}
	items := r.OldestFirst()
	for since := uint64(0); since <= 8; since++ {
		a, b := r.Since(since, 3), SliceSince(items, r.total, r.epoch, since, 3)
		if a.Reset != b.Reset || a.More != b.More || a.Cursor != b.Cursor || !slices.Equal(a.Items, b.Items) {
			t.Fatalf("since %d: %+v vs %+v", since, a, b)
		}
	}
}

type item struct {
	id string
	v  int
}

func TestVersions(t *testing.T) {
	v := NewVersions(func(i item) string { return i.id }, func(a, b item) bool { return a == b })
	apply := func(have []item, d Delta[item]) []item {
		if d.Full {
			return slices.Clone(d.Changed)
		}
		have = slices.DeleteFunc(have, func(i item) bool { return slices.Contains(d.Removed, i.id) })
		for _, c := range d.Changed {
			if j := slices.IndexFunc(have, func(i item) bool { return i.id == c.id }); j >= 0 {
				have[j] = c
			} else {
				have = append(have, c)
			}
		}
		return have
	}
	lists := [][]item{
		{{"a", 1}, {"b", 1}},
		{{"a", 2}, {"b", 1}, {"c", 1}},
		{{"b", 1}, {"c", 1}},
		{{"b", 1}, {"c", 2}, {"d", 1}},
	}
	var have []item
	var cur uint64
	for i, l := range lists {
		v.Update(l)
		d := v.Since(l, cur)
		if i > 0 && d.Full {
			t.Fatalf("step %d: full", i)
		}
		if i == 2 && (len(d.Changed) != 0 || !slices.Equal(d.Removed, []string{"a"})) {
			t.Fatalf("step 2: %+v", d)
		}
		if i == 3 && len(d.Changed) != 2 {
			t.Fatalf("step 3 should send c and d only: %+v", d)
		}
		have, cur = apply(have, d), d.Cursor
		if !slices.Equal(have, l) {
			t.Fatalf("step %d: have %v want %v", i, have, l)
		}
	}
	// A client from long ago gets the whole list.
	for range keepRemovals + 2 {
		v.Update([]item{{"z", 1}})
	}
	if d := v.Since([]item{{"z", 1}}, cur); !d.Full {
		t.Fatalf("stale cursor: %+v", d)
	}
}
