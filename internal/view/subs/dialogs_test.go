package subs

import "testing"

// A stored interval the server chose (e.g. 8 h) shows as the nearest
// offered choice, never as 关闭; only ≤ 0 is 关闭 (docs/design.md:345).
func TestAutoIndexSnapsToNearestChoice(t *testing.T) {
	cases := map[int]int{0: 0, -1: 0, 1: 1, 6: 1, 8: 1, 9: 1, 10: 2, 12: 2, 18: 2, 19: 3, 24: 3, 72: 3}
	for hours, want := range cases {
		if got := autoIndex(hours); got != want {
			t.Errorf("autoIndex(%d) = %d (%s), want %d (%s)", hours, got, autoLabels[got], want, autoLabels[want])
		}
	}
	// Saving the dialog round-trips a snapped value to an offered one.
	if autoHours[autoIndex(8)] != 6 {
		t.Fatalf("8 h saves as %d", autoHours[autoIndex(8)])
	}
}
