package view

import (
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/app/fake"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
)

// snapshot is one frame to render: a page, an optional navigation arg
// and a preparation step that drives the UI (or the fake) into a state.
type snapshot struct {
	name string
	page env.Page
	arg  string
	prep func(t *testing.T, api *fake.Fake, s *Shell, tt *ui.Tester)
}

// snapshots lists every page plus the key states a design review needs:
// dialogs, empty states, errors, toasts, the probe tabs with results, the
// stats dimensions and the topology hover (docs/design.md §11).
var snapshots = []snapshot{
	// Pages as the fake starts them: core running, three subscriptions.
	{name: "overview", page: env.Overview},
	{name: "proxies", page: env.Proxies},
	{name: "subs", page: env.Subs},
	{name: "conns", page: env.Conns},
	{name: "conns-detail", page: env.Conns, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, "www.google.com:443")
	}},
	{name: "conns-closed", page: env.Conns, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("已关闭"))
	}},
	{name: "conns-topology", page: env.Conns, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("拓扑"))
	}},
	{name: "conns-topology-hover", page: env.Conns, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("拓扑"))
		tt.Frame()
		if r, ok := tt.Find(i18n.T("网络拓扑")); ok {
			// Hover the middle of the first column (the device nodes).
			tt.Move(r.X+r.W*0.08, r.Y+r.H*0.5)
			tt.Frame()
		}
	}},
	{name: "logs", page: env.Logs},
	{name: "rules", page: env.Rules},
	{name: "rules-add", page: env.Rules, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("添加规则"))
	}},
	{name: "rules-test", page: env.Rules, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("测试…"))
	}},
	{name: "net", page: env.Net},
	{name: "net-leak", page: env.Net, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("开始检查"))
		tt.Frame()
		_ = tt.Click(i18n.T("仅遵守系统代理的应用由澜流解析域名"))
		_ = tt.Click(i18n.T("查询"))
	}},
	{name: "stats", page: env.Stats},
	{name: "stats-domain", page: env.Stats, prep: clickOnly("域名")},
	{name: "stats-process", page: env.Stats, prep: clickOnly("进程")},
	{name: "stats-rule", page: env.Stats, prep: clickOnly("规则")},
	{name: "stats-proxy", page: env.Stats, prep: clickOnly("代理|统计")},
	{name: "stats-country", page: env.Stats, prep: clickOnly("地区")},
	{name: "stats-inbound", page: env.Stats, prep: clickOnly("入站")},
	{name: "stats-today", page: env.Stats, prep: clickOnly("今天")},
	{name: "stats-empty", page: env.Stats, prep: func(t *testing.T, api *fake.Fake, _ *Shell, _ *ui.Tester) {
		if err := api.ResetTrafficStats(); err != nil {
			t.Fatal(err)
		}
	}},
	{name: "probe", page: env.Probe},
	{name: "probe-ip-result", page: env.Probe, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("检测出口"))
		tt.Frame()
		click(t, tt, i18n.T("在线检测"))
	}},
	{name: "probe-unlock", page: env.Probe, prep: clickOnly("解锁检测")},
	{name: "probe-unlock-result", page: env.Probe, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("解锁检测"))
		tt.Frame()
		click(t, tt, i18n.T("开始检测"))
	}},
	{name: "probe-region", page: env.Probe, prep: clickOnly("地区探查")},
	{name: "probe-region-result", page: env.Probe, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("地区探查"))
		tt.Frame()
		click(t, tt, i18n.T("开始探查"))
	}},
	{name: "settings-general", page: env.Settings, arg: "general"},
	{name: "settings-proxy", page: env.Settings, arg: "proxy"},
	{name: "settings-dns", page: env.Settings, arg: "dns"},
	{name: "settings-sniffer", page: env.Settings, arg: "sniffer"},
	{name: "settings-override", page: env.Settings, arg: "override"},
	{name: "settings-kernel", page: env.Settings, arg: "kernel"},
	{name: "settings-advanced", page: env.Settings, arg: "advanced"},

	// Dialogs.
	{name: "dialog-import", page: env.Subs, arg: "import-url"},
	{name: "dialog-edit-sub", page: env.Subs, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("更多 ")+"备用机场")
		if err := tt.ChooseMenuItem(i18n.T("编辑…")); err != nil {
			t.Fatal(err)
		}
	}},
	{name: "dialog-view-config", page: env.Subs, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("更多 ")+"备用机场")
		if err := tt.ChooseMenuItem(i18n.T("查看配置")); err != nil {
			t.Fatal(err)
		}
	}},
	{name: "dialog-delete-sub", page: env.Subs, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("更多 ")+"备用机场")
		if err := tt.ChooseMenuItem(i18n.T("删除…")); err != nil {
			t.Fatal(err)
		}
	}},
	{name: "dialog-invalid-config", page: env.Subs, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		click(t, tt, i18n.T("更多 ")+"备用机场")
		if err := tt.ChooseMenuItem(i18n.T("使用此订阅")); err != nil {
			t.Fatal(err)
		}
	}},
	{name: "dialog-override-editor", page: env.Settings, arg: "override:global"},
	{name: "dialog-runtime-config", page: env.Settings, arg: "override", prep: clickOnly("查看…")},
	{name: "dialog-atrust", page: env.Settings, arg: "proxy", prep: clickOnly("配置…")},
	{name: "dialog-bypass", page: env.Settings, arg: "proxy", prep: clickOnly("编辑…")},
	{name: "dialog-tun-helper", page: env.Settings, arg: "proxy", prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		clickSwitch(t, tt, i18n.T("TUN 模式（增强模式）"))
	}},
	{name: "dialog-core-install", page: env.Settings, arg: "kernel", prep: clickOnly("安装其他版本…")},
	{name: "dialog-export", page: env.Settings, arg: "general", prep: clickOnly("导出…")},
	{name: "dialog-webdav", page: env.Settings, arg: "general", prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		clickScrolled(t, tt, i18n.T("配置…"))
	}},
	{name: "dialog-webdav-list", page: env.Settings, arg: "general", prep: func(t *testing.T, api *fake.Fake, _ *Shell, tt *ui.Tester) {
		if err := api.SetWebDAVPassword("secret"); err != nil {
			t.Fatal(err)
		}
		set := api.Settings()
		set.WebDAV.URL = "https://dav.example.com/lanflow"
		set.WebDAV.User = "me"
		if err := api.SaveSettings(set); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		clickScrolled(t, tt, i18n.T("管理备份…"))
	}},
	{name: "dialog-hosts", page: env.Settings, arg: "dns", prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		if err := tt.Click(i18n.T("编辑…")); err != nil {
			t.Logf("hosts: %v", err)
		}
	}},
	{name: "dialog-add-site", page: env.Net, prep: clickOnly("＋ 添加网站")},
	{name: "dialog-quit", page: env.Overview, prep: func(_ *testing.T, _ *fake.Fake, s *Shell, _ *ui.Tester) {
		s.AskQuit(func() {})
	}},
	{name: "dialog-recovery", page: env.Overview, prep: func(_ *testing.T, _ *fake.Fake, s *Shell, _ *ui.Tester) {
		s.recovery = true
	}},

	// Toasts and errors.
	{name: "toast-ok", page: env.Overview, prep: func(_ *testing.T, _ *fake.Fake, s *Shell, tt *ui.Tester) {
		s.ToggleSystemProxy()
		settleToast(tt)
	}},
	{name: "toast-error", page: env.Subs, prep: func(_ *testing.T, _ *fake.Fake, s *Shell, tt *ui.Tester) {
		s.run(func() error {
			return errors.New(i18n.T("订阅更新失败：连接超时。仍在使用上次的配置。"))
		}, "")
		settleToast(tt)
	}},
	{name: "toast-error-detail", page: env.Subs, prep: func(t *testing.T, _ *fake.Fake, s *Shell, tt *ui.Tester) {
		s.run(func() error {
			return errors.New(i18n.T("订阅更新失败：连接超时。仍在使用上次的配置。"))
		}, "")
		tt.Frame()
		click(t, tt, i18n.T("查看详情"))
	}},
	{name: "toast-core-crash", page: env.Overview, prep: func(_ *testing.T, _ *fake.Fake, s *Shell, tt *ui.Tester) {
		s.hiddenOnce, s.hiddenFlag = false, ""
		s.handleNotice(nil, app.Notice{Kind: "core-crash", Detail: "exit status 1"})
		settleToast(tt)
	}},

	// Empty states: the core stopped, then the first run.
	{name: "empty-overview-stopped", page: env.Overview, prep: stopCore},
	{name: "empty-proxies-stopped", page: env.Proxies, prep: stopCore},
	{name: "empty-conns-stopped", page: env.Conns, prep: stopCore},
	{name: "empty-rules-stopped", page: env.Rules, prep: stopCore},
	{name: "empty-net-stopped", page: env.Net, prep: stopCore},
	{name: "empty-probe-stopped", page: env.Probe, prep: stopCore},
	{name: "empty-logs-cleared", page: env.Logs, prep: func(_ *testing.T, api *fake.Fake, _ *Shell, _ *ui.Tester) { api.ClearLogs() }},
	{name: "empty-overview-first-run", page: env.Overview, prep: snapFirstRun},
	{name: "empty-proxies-first-run", page: env.Proxies, prep: snapFirstRun},
	{name: "empty-subs-first-run", page: env.Subs, prep: snapFirstRun},
	{name: "empty-conns-search", page: env.Conns, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		tt.Key(ui.Cmd, ui.KeyF)
		tt.Type("zzzz")
	}},
	{name: "empty-proxies-search", page: env.Proxies, prep: func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) {
		tt.Key(ui.Cmd, ui.KeyF)
		tt.Type("zzzz")
	}},
	{name: "mode-direct-overview", page: env.Overview, prep: setMode(model.ModeDirect)},
	{name: "mode-global-overview", page: env.Overview, prep: setMode(model.ModeGlobal)},
	{name: "mode-direct-proxies", page: env.Proxies, prep: setMode(model.ModeDirect)},
}

// settleToast lets a toast finish fading in (200 ms) before the frame.
func settleToast(tt *ui.Tester) {
	tt.Frame()
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
}

func clickOnly(key string) func(*testing.T, *fake.Fake, *Shell, *ui.Tester) {
	return func(t *testing.T, _ *fake.Fake, _ *Shell, tt *ui.Tester) { click(t, tt, i18n.T(key)) }
}

// clickScrolled scrolls the content area until text is inside the window,
// then clicks it (settings tabs scroll at the minimum window size).
func clickScrolled(t *testing.T, tt *ui.Tester, text string) {
	t.Helper()
	for i := 0; i < 8; i++ {
		r, ok := tt.Find(text)
		if !ok {
			t.Fatalf("find %q: %q", text, tt.Texts())
		}
		if r.Y+r.H <= float32(tt.Image().Bounds().Dy())-20 && r.Y >= 0 {
			break
		}
		tt.Scroll(r.X, 400, 0, 300)
		tt.Frame()
	}
	click(t, tt, text)
}

func clickSwitch(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	info, ok := tt.AccessByRole(ui.RoleSwitch, label)
	if !ok {
		t.Fatalf("no switch %q: %q", label, tt.Texts())
	}
	tt.ClickAt(info.Bounds.X+info.Bounds.W/2, info.Bounds.Y+info.Bounds.H/2)
}

func stopCore(t *testing.T, api *fake.Fake, _ *Shell, _ *ui.Tester) {
	if err := api.StopCore(); err != nil {
		t.Fatal(err)
	}
}

func snapFirstRun(_ *testing.T, api *fake.Fake, _ *Shell, _ *ui.Tester) { api.ClearSubscriptions() }

func setMode(m model.Mode) func(*testing.T, *fake.Fake, *Shell, *ui.Tester) {
	return func(t *testing.T, api *fake.Fake, _ *Shell, _ *ui.Tester) {
		if err := api.SetMode(m); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSnapshots renders every page and state of snapshots at the default
// and the minimum window size, in light and dark mode, and writes the
// frames as PNG files to the directory in LANFLOW_SNAP_DIR (a review aid
// for layout work; skipped otherwise). LANFLOW_SNAP_ONLY limits the run
// to snapshots whose name contains it.
func TestSnapshots(t *testing.T) {
	dir := os.Getenv("LANFLOW_SNAP_DIR")
	if dir == "" {
		t.Skip("set LANFLOW_SNAP_DIR to write snapshots")
	}
	only := os.Getenv("LANFLOW_SNAP_ONLY")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sizes := []struct {
		name string
		w, h int
	}{{"default", DefaultWidth, DefaultHeight}, {"min", MinWidth, MinHeight}}
	themes := []struct {
		name string
		dark bool
	}{{"light", false}, {"dark", true}}
	for _, th := range themes {
		for _, sz := range sizes {
			for _, sh := range snapshots {
				if only != "" && !strings.Contains(sh.name, only) {
					continue
				}
				api, s, tt := newTest(t)
				tt.SetTitleBar(ui.TitleBar{Height: 52})
				tt.SetDark(th.dark)
				tt.SetSize(sz.w, sz.h)
				s.navigate(sh.page, sh.arg)
				tt.Frame()
				if sh.prep != nil {
					sh.prep(t, api, s, tt)
					tt.Frame()
				}
				name := filepath.Join(dir, sh.name+"-"+sz.name+"-"+th.name+".png")
				f, err := os.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				if err := png.Encode(f, tt.Image()); err != nil {
					t.Fatal(err)
				}
				f.Close()
			}
		}
	}
}
