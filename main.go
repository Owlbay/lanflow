// 澜流 Lanflow: a Mihomo desktop client with MyGo native UI.
package main

import (
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/egoist/mygo"
	_ "github.com/egoist/mygo/locales/zh-Hans" // MyGo's own strings in Chinese
	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/app/fake"
	"lanflow/internal/i18n"
	"lanflow/internal/view"
	"lanflow/internal/view/env"
)

// backend picks the service: the in-memory fake with LANFLOW_FAKE=1, the
// real service otherwise (main_service.go); the fake if that fails. lang
// is the language of a first run.
func backend(lang string) app.API {
	if os.Getenv("LANFLOW_FAKE") == "1" {
		return fake.New()
	}
	api, err := newService(lang)
	if err != nil {
		log.Printf("lanflow: service unavailable, using the fake backend: %v", err)
		return fake.New()
	}
	return api
}

func main() {
	if code, ok := runSubcommand(os.Args[1:]); ok {
		os.Exit(code)
	}
	if !mygo.App.RequestSingleInstanceLock() {
		return // the running instance takes over
	}
	mygo.App.SetName("Lanflow")
	// The app is written in Chinese and English (docs/design.md §5.8 语言);
	// a first run follows the system's language, afterwards the setting
	// rules, for the app's own texts (i18n) and MyGo's (SetLocale).
	mygo.App.SetLanguages("zh-Hans", "en")
	api := backend(i18n.FromSystem(mygo.App.PreferredLanguages()))
	// Lanflow's own log.Printf lines also go to lanflow.log (internal/applog).
	if w, ok := api.(interface{ AppLogWriter() io.Writer }); ok {
		log.SetOutput(io.MultiWriter(os.Stderr, w.AppLogWriter()))
	}
	i18n.SetLang(api.Settings().Language)
	view.ApplyLanguage(api.Settings().Language)
	view.ApplyAppearance(api.Settings().Appearance)
	shell := view.New(api, view.Options{Mac: runtime.GOOS == "darwin"})
	stopLocalAPI := startLocalAPI(api, agentPrompt(shell))
	if p := os.Getenv("LANFLOW_PAGE"); p != "" && os.Getenv("LANFLOW_FAKE") == "1" {
		// Development aid: open on a page, e.g. "subs" or "subs:import-url".
		page, arg, _ := strings.Cut(p, ":")
		shell.Env().Navigate(env.Page(page), arg)
	}

	var win *mygo.Window
	quitting := false
	showWindow := func() {
		if win != nil {
			shell.ShowWindow()
		}
	}

	// The tray keeps the app alive once the window is hidden.
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.OnSecondInstance(func([]string, string) { showWindow() })
	mygo.App.OnActivate(func(hasVisibleWindows bool) {
		if !hasVisibleWindows {
			showWindow()
		}
	})
	// Deep links (lanflow://import?url=…, clash://install-config?url=…) open
	// the import dialog with the link filled in; registered before Run so
	// the URL the app was launched with arrives too (docs/design.md §9 / §6.1).
	mygo.App.OnOpenURL(shell.OpenImportLink)
	// Wake and connectivity changes from the OS; the service also detects
	// them itself (internal/app/watch.go) where these events are missing.
	if w, ok := api.(interface {
		Resumed()
		NetworkChanged()
	}); ok {
		mygo.Power.OnResume(w.Resumed)
		mygo.Network.OnChanged(func(mygo.NetworkStatus) { w.NetworkChanged() })
	}
	// A termination signal (pkill, logout scripts, an installer replacing
	// the app) must not wait on the quit confirmation nobody will answer:
	// quit straight away; OnWillQuit still restores the system proxy and DNS.
	var signaled atomic.Bool
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		signaled.Store(true)
		mygo.App.Quit()
	}()
	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
		if quitting || signaled.Load() {
			quitting = true
			return
		}
		// The confirmation (docs/design.md §6.4) also applies to quitting
		// from the tray while the window is hidden: bring it up and ask.
		if win != nil && shell.NeedsQuitConfirm() {
			e.PreventDefault()
			showWindow()
			shell.AskQuit(func() {
				quitting = true
				mygo.App.Quit()
			})
			return
		}
		quitting = true
	})
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		stopLocalAPI()
		shell.Close()
		api.Shutdown()
	})

	mygo.App.WhenReady(func() {
		settings := api.Settings()
		// The default size fits the pages without page-level scrolling
		// (docs/design.md §4); on a first launch it is clamped to the
		// primary display's work area. StateKey restores the size and
		// position of the last run over these.
		width, height := view.FitWorkArea(mygo.Screen.PrimaryDisplay().WorkArea)
		opts := mygo.WindowOptions{
			Title:     i18n.T("澜流 Lanflow"),
			Width:     width,
			Height:    height,
			MinWidth:  view.MinWidth,
			MinHeight: view.MinHeight,
			StateKey:  "main",
			Hidden:    settings.SilentStart,
			Content:   ui.View(shell.View),
		}
		if runtime.GOOS == "darwin" {
			opts.TitleBarStyle = mygo.TitleBarHiddenInset
			opts.Vibrancy = mygo.VibrancySidebar
		}
		win = mygo.NewWindow(opts)
		shell.Attach(win)
		win.OnClose(func(e *mygo.CloseEvent) {
			if quitting {
				return
			}
			if api.Settings().CloseToTray {
				e.PreventDefault()
				win.Hide()
				shell.HiddenToTray()
				return
			}
			mygo.App.Quit()
		})
		if err := shell.StartTray(); err != nil {
			log.Printf("lanflow: tray: %v", err)
		}
		if err := shell.RegisterGlobalShortcut(); err != nil {
			log.Printf("lanflow: global shortcut: %v", err)
		}
		for action, err := range shell.RegisterShortcuts() { // the other actions (view/shortcuts.go)
			log.Printf("lanflow: shortcut %s: %v", action, err)
		}
		shell.CheckUpdatesAtLaunch()
		if runtime.GOOS != "darwin" {
			// Portable builds have no installer to register the scheme.
			if err := mygo.App.RegisterURLScheme("lanflow"); err != nil {
				log.Printf("lanflow: url scheme: %v", err)
			}
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
