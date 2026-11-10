package protocol

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		cmp  int
		ok   bool
	}{
		{"1.2.3", "1.2.3", 0, true},
		{"v1.2.3", "1.2.3", 0, true},
		{"1.2.3", "1.10.0", -1, true},
		{"2.0.0", "1.99.99", 1, true},
		{"1.0.0-rc.1", "1.0.0", -1, true},
		{"1.0.0+abc", "1.0.0", 0, true},
		{"dev", "1.0.0", 0, false},
		{"1.0", "1.0.0", 0, false},
		{"", "", 0, false},
	}
	for _, c := range cases {
		cmp, ok := CompareVersions(c.a, c.b)
		if cmp != c.cmp || ok != c.ok {
			t.Errorf("CompareVersions(%q, %q) = %d %v, want %d %v", c.a, c.b, cmp, ok, c.cmp, c.ok)
		}
	}
}

func TestParseVersionLine(t *testing.T) {
	if v, ok := ParseVersionLine("lanflow-helper 0.3.1 (protocol 1)\n"); !ok || v != "0.3.1" {
		t.Fatalf("got %q %v", v, ok)
	}
	if _, ok := ParseVersionLine("something else"); ok {
		t.Fatal("parsed garbage")
	}
}
