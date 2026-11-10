package main

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app/fake"
	"lanflow/internal/view"
)

// TestWindowOpens builds the whole window over the fake backend, as the
// app does, and visits every page.
func TestWindowOpens(t *testing.T) {
	api := fake.NewInstant()
	shell := view.New(api, view.Options{Mac: true, Sync: true})
	defer shell.Close()
	tt := ui.NewTester(shell.View, view.DefaultWidth, view.DefaultHeight)
	if !tt.HasText("概览") || !tt.HasText("出口地图") {
		t.Fatalf("overview not shown: %q", tt.Texts())
	}
	for _, page := range []string{"代理", "订阅", "连接", "日志", "规则", "网络", "设置", "概览"} {
		if err := tt.Click(page); err != nil {
			t.Fatalf("click %s: %v", page, err)
		}
	}
}
