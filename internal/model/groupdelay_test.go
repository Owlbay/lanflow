package model

import (
	"slices"
	"testing"
)

func TestGroupFastestDelay(t *testing.T) {
	s := ProxySnapshot{
		Groups: []ProxyGroup{
			{Name: "选择", Now: "DIRECT", All: []string{"DIRECT", "自动", "a"}},
			{Name: "自动", All: []string{"b", "c", "选择"}}, // cycle back to 选择
			{Name: "空", All: []string{"REJECT"}},
			{Name: "坏", All: []string{"d"}},
		},
		Proxies: map[string]Proxy{
			"a": {Delay: 300}, "b": {Delay: 120, Testing: true}, "c": {Delay: -1}, "d": {Delay: -1},
			"选择": {Delay: 999}, // the core's own (current-member) delay is replaced
		},
	}
	if got := s.GroupNodes("选择"); !slices.Equal(got, []string{"b", "c", "a"}) {
		t.Fatalf("GroupNodes = %v", got)
	}
	if got := s.GroupNodes("a"); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("node GroupNodes = %v", got)
	}
	s.ApplyGroupDelays()
	for name, want := range map[string]Proxy{
		"选择": {Delay: 120, Testing: true},
		"自动": {Delay: 120, Testing: true},
		"空":  {Name: "空"},
		"坏":  {Name: "坏", Delay: -1},
	} {
		got := s.Proxies[name]
		if got.Delay != want.Delay || got.Testing != want.Testing {
			t.Errorf("%s = %+v, want delay %d testing %v", name, got, want.Delay, want.Testing)
		}
	}
}
