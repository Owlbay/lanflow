package profile

import (
	"strings"

	"lanflow/internal/model"
)

// maxDiffD bounds the Myers search; beyond it the texts are reported as a
// full replacement, which keeps memory at O(maxDiffD²).
const maxDiffD = 3000

func splitLines(b []byte) []string {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// diff is a Myers O(ND) line diff with common prefix/suffix trimming.
func diff(a, b []byte) []model.DiffLine {
	la, lb := splitLines(a), splitLines(b)

	// Trim common prefix and suffix.
	pre := 0
	for pre < len(la) && pre < len(lb) && la[pre] == lb[pre] {
		pre++
	}
	suf := 0
	for suf < len(la)-pre && suf < len(lb)-pre && la[len(la)-1-suf] == lb[len(lb)-1-suf] {
		suf++
	}
	midA, midB := la[pre:len(la)-suf], lb[pre:len(lb)-suf]

	out := make([]model.DiffLine, 0, len(la)+len(lb))
	for _, l := range la[:pre] {
		out = append(out, model.DiffLine{Op: ' ', Text: l})
	}
	out = append(out, myers(midA, midB)...)
	for _, l := range la[len(la)-suf:] {
		out = append(out, model.DiffLine{Op: ' ', Text: l})
	}
	return out
}

func myers(a, b []string) []model.DiffLine {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil
	}
	replaceAll := func() []model.DiffLine {
		out := make([]model.DiffLine, 0, n+m)
		for _, l := range a {
			out = append(out, model.DiffLine{Op: '-', Text: l})
		}
		for _, l := range b {
			out = append(out, model.DiffLine{Op: '+', Text: l})
		}
		return out
	}
	if n == 0 || m == 0 {
		return replaceAll()
	}

	// Intern lines so comparisons are integer compares.
	ids := map[string]int32{}
	intern := func(ls []string) []int32 {
		out := make([]int32, len(ls))
		for i, l := range ls {
			id, ok := ids[l]
			if !ok {
				id = int32(len(ids))
				ids[l] = id
			}
			out[i] = id
		}
		return out
	}
	ia, ib := intern(a), intern(b)

	maxD := n + m
	if maxD > maxDiffD {
		maxD = maxDiffD
	}
	offset := maxD
	v := make([]int, 2*maxD+2)
	var trace [][]int
	found := false
	for d := 0; d <= maxD && !found; d++ {
		snapshot := make([]int, len(v))
		copy(snapshot, v)
		trace = append(trace, snapshot)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[offset+k-1] < v[offset+k+1]) {
				x = v[offset+k+1]
			} else {
				x = v[offset+k-1] + 1
			}
			y := x - k
			for x < n && y < m && ia[x] == ib[y] {
				x++
				y++
			}
			v[offset+k] = x
			if x >= n && y >= m {
				found = true
				break
			}
		}
	}
	if !found {
		return replaceAll()
	}

	// Backtrack.
	type op struct {
		kind byte
		ai   int
		bi   int
	}
	var ops []op
	x, y := n, m
	for d := len(trace) - 1; d >= 0; d-- {
		vv := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && vv[offset+k-1] < vv[offset+k+1]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := vv[offset+prevK]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			x--
			y--
			ops = append(ops, op{' ', x, y})
		}
		if d > 0 {
			if x == prevX {
				y--
				ops = append(ops, op{'+', 0, y})
			} else {
				x--
				ops = append(ops, op{'-', x, 0})
			}
		}
	}
	out := make([]model.DiffLine, 0, len(ops))
	for i := len(ops) - 1; i >= 0; i-- {
		o := ops[i]
		switch o.kind {
		case ' ':
			out = append(out, model.DiffLine{Op: ' ', Text: a[o.ai]})
		case '-':
			out = append(out, model.DiffLine{Op: '-', Text: a[o.ai]})
		case '+':
			out = append(out, model.DiffLine{Op: '+', Text: b[o.bi]})
		}
	}
	return out
}
