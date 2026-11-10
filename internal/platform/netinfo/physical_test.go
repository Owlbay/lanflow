package netinfo

import (
	"context"
	"errors"
	"testing"
)

func TestPickPhysicalSkipsTunnels(t *testing.T) {
	cases := []struct {
		name string
		in   []candidate
		want string
	}{
		{"utun first in table", []candidate{
			{name: "utun1500", index: 30, metric: 0},
			{name: "en0", index: 12, metric: 1},
		}, "en0"},
		{"tun and wintun", []candidate{
			{name: "tun0", index: 5, metric: 0},
			{name: "wintun", index: 6, metric: 1},
			{name: "Meta", index: 7, metric: 2},
			{name: "Mihomo", index: 8, metric: 3},
			{name: "Wi-Fi", index: 9, metric: 50},
		}, "Wi-Fi"},
		{"virtual by type", []candidate{
			{name: "Ethernet 3", index: 4, metric: 1, tunnel: true},
			{name: "Ethernet", index: 3, metric: 25},
		}, "Ethernet"},
		{"lowest metric wins", []candidate{
			{name: "Wi-Fi", index: 9, metric: 50},
			{name: "Ethernet", index: 3, metric: 25},
		}, "Ethernet"},
		{"no index", []candidate{
			{name: "en1", index: 0, metric: 0},
			{name: "en0", index: 12, metric: 1},
		}, "en0"},
	}
	for _, tc := range cases {
		got, err := pickPhysical(tc.in)
		if err != nil || got.name != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.name, got.name, err, tc.want)
		}
	}
}

func TestPickPhysicalNone(t *testing.T) {
	for _, in := range [][]candidate{
		nil,
		{{name: "utun4", index: 22}, {name: "wintun", index: 3}, {name: "tun0", index: 2}},
		{{name: "Ethernet 3", index: 4, tunnel: true}},
	} {
		if _, err := pickPhysical(in); !errors.Is(err, ErrNoPhysical) {
			t.Errorf("pickPhysical(%v) err = %v, want ErrNoPhysical", in, err)
		}
	}
}

func TestPhysicalInterfaceSmoke(t *testing.T) {
	p, err := PhysicalInterface(context.Background())
	if err != nil {
		t.Skipf("no physical interface here: %v", err)
	}
	if p.Index <= 0 || isVirtualName(p.Name) {
		t.Fatalf("PhysicalInterface = %+v", p)
	}
	t.Logf("physical: %+v", p)
}
