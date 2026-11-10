package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/egoist/mygo"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/rpc"
	"lanflow/internal/split"
	"lanflow/internal/view"
	"lanflow/internal/view/env"
)

// The two processes of docs/service-split.md S2.

// ---- the service process ----

// runService is `Lanflow --service`: app.Service, the tray, the global
// shortcuts, the system notifications and the local interface, without a
// window (and without a Dock icon on macOS). It holds the single-instance
// lock; the window processes connect to it and it starts one on demand.
func runService(args split.Args) {
	if !mygo.App.RequestSingleInstanceLock() {
		return // a service runs already; it got our arguments
	}
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	api := backend(i18n.FromSystem(mygo.App.PreferredLanguages()))
	if c, ok := api.(interface{ CatchPanic(where string) }); ok {
		defer c.CatchPanic("main")
	}
	setupLogging(api)
	i18n.SetLang(api.Settings().Language)
	exe := executable()

	box := split.NewNotices(api)
	var srv *rpc.Server
	var startMu sync.Mutex
	var lastStart time.Time
	showWindow := func(arg string) {
		if srv != nil && srv.SendUI(rpc.UIShow, arg) {
			return
		}
		// A window process may be on its way (a double click on the
		// icon): one start per second at most.
		startMu.Lock()
		if time.Since(lastStart) < time.Second && arg == "" {
			startMu.Unlock()
			return
		}
		lastStart = time.Now()
		startMu.Unlock()
		if err := split.StartWindow(exe, arg); err != nil {
			log.Printf("lanflow: start the window: %v", err)
		}
	}
	var confirmed, signaled atomic.Bool
	quitNow := func() {
		confirmed.Store(true)
		mygo.App.Quit()
	}
	bg := view.NewBackground(box, view.BackgroundOptions{
		Mac:        runtime.GOOS == "darwin",
		ShowWindow: showWindow,
		ToggleWindow: func() {
			if srv == nil || !srv.SendUI(rpc.UIToggle, "") {
				showWindow("")
			}
		},
		Quit:     func() { mygo.App.Quit() },
		Settings: func(set model.Settings) { queueAutostart(exe, set.OpenAtLogin) },
	})
	box.OnNew = bg.SystemNotice
	offNotices := api.OnChange(func(t app.Topic) {
		if t == app.TopicNotice {
			box.Pull()
		}
	})
	host := &serviceHost{api: api, bg: bg, quit: quitNow}
	host.SetBackground(true) // no window yet
	srv = serveLocalAPI(box, backgroundPrompt(bg), host)
	if srv == nil {
		// Another process serves the endpoint: a single-process Lanflow
		// (an older version). Nothing to do here.
		log.Printf("lanflow: service: the local interface is taken; quitting")
		api.Shutdown()
		return
	}

	mygo.App.OnWindowAllClosed(func() {}) // dialogs only; never quit for them
	mygo.App.OnSecondInstance(func(args []string, _ string) {
		a := split.ParseArgs(args)
		if a.Service || a.Fallback {
			return // the login item while running; a window that gave up on us
		}
		showWindow(firstOf(a.Open, a.Link))
	})
	// Finder, the Dock or Spotlight open the app while only the service
	// runs: LaunchServices activates this process instead of starting the
	// app again. Bring up the window.
	mygo.App.OnActivate(func(bool) { showWindow("") })
	mygo.App.OnOpenURL(func(u string) { showWindow(u) })
	if w, ok := api.(interface {
		Resumed()
		NetworkChanged()
	}); ok {
		mygo.Power.OnResume(w.Resumed)
		mygo.Network.OnChanged(func(mygo.NetworkStatus) { w.NetworkChanged() })
	}
	var cleanup sync.Once
	finish := func() {
		cleanup.Do(func() {
			srv.SendUI(rpc.UIQuit, "")
			offNotices()
			_ = srv.Close()
			bg.Close()
			api.Shutdown()
		})
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		signaled.Store(true)
		mygo.App.Quit()
		// The main thread may be stuck (a system prompt): restore the
		// proxy and go anyway.
		time.Sleep(quitWatchdog)
		log.Printf("lanflow: service: quit did not finish in %s; exiting", quitWatchdog)
		finish()
		os.Exit(0)
	}()
	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
		if confirmed.Load() || signaled.Load() {
			return
		}
		// 退出澜流 from the tray (docs/design.md §6.4): the window asks when
		// it is open, a native dialog otherwise.
		e.PreventDefault()
		if srv.SendUI(rpc.UIAskQuit, "") {
			return // the window calls QuitService when the user agrees
		}
		go func() {
			if bg.ConfirmQuit() {
				quitNow()
			}
		}()
	})
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { finish() })
	mygo.App.WhenReady(func() {
		bg.Start()
		registerURLScheme()
		if args.Login && !api.Settings().SilentStart {
			showWindow("") // started at login: the window too, as before the split
		} else if args.Open != "" || args.Link != "" {
			showWindow(firstOf(args.Open, args.Link))
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// serviceHost is the service process's side of the window ops (rpc.Host).
type serviceHost struct {
	api  app.API
	bg   *view.Background
	quit func()
}

func (*serviceHost) Split() bool       { return true }
func (*serviceHost) ShowWindow(string) {}
func (h *serviceHost) Quit()           { h.quit() }
func (h *serviceHost) SetShortcut(action, acc string) error {
	return h.bg.SetShortcut(action, acc)
}

// SetBackground paces the service's streams by the window: slow while no
// window is open or it is hidden (app.Service.SetBackground).
func (h *serviceHost) SetBackground(hidden bool) {
	if b, ok := h.api.(interface{ SetBackground(bool) }); ok {
		b.SetBackground(hidden)
	}
}

// backgroundPrompt asks for agents' consent through the tray and a
// system notification (view.Background.AskAgentConsent).
func backgroundPrompt(bg *view.Background) rpc.PromptFunc {
	return func(ctx context.Context, c rpc.Consent) (rpc.Answer, error) {
		ch := make(chan string, 1)
		withdraw := bg.AskAgentConsent(view.AgentConsent{Client: c.Client, Agent: c.Agent, Kind: string(c.Kind), Summary: c.Summary},
			func(a string) { ch <- a })
		select {
		case a := <-ch:
			return rpc.Answer(a), nil
		case <-ctx.Done():
			withdraw()
			return "", ctx.Err()
		}
	}
}

// ---- the window process ----

// runWindow is a plain launch: the window, over the local interface of
// the service process, which it starts when needed. Closing the window
// ends this process; the service, the core and the connections stay.
func runWindow(args split.Args) {
	exe := executable()
	o := app.DefaultOptions("Lanflow")
	version := mygo.App.Version()

	var (
		shell           *view.Shell
		shellMu         sync.Mutex
		pendingOpen     []string // asked for before the window existed
		serviceQuitting atomic.Bool
		restarting      atomic.Int64 // pid of a service being replaced, 0 = none
		closingWindow   atomic.Bool
		quitting        atomic.Bool
		signaled        atomic.Bool
	)
	withShell := func(fn func(*view.Shell)) {
		shellMu.Lock()
		s := shell
		shellMu.Unlock()
		if s != nil {
			fn(s)
		}
	}
	open := func(arg string) {
		shellMu.Lock()
		s := shell
		if s == nil {
			pendingOpen = append(pendingOpen, arg)
		}
		shellMu.Unlock()
		if s != nil {
			s.ShowWindow()
			s.Open(arg)
		}
	}
	var client interface{ QuitService() error }
	var detachFn atomic.Pointer[func()] // DetachUI, once connected
	detach := func() {
		if fn := detachFn.Load(); fn != nil {
			(*fn)()
		}
	}
	// leaving: this window process is on its way out while the service
	// stays (the window was closed, a signal).
	leaving := func() bool {
		return closingWindow.Load() || signaled.Load() || (quitting.Load() && !serviceQuitting.Load())
	}
	// handOff passes a request for the window to a new window process
	// when this one is leaving: the service forgets this one first, so the
	// new one becomes the window.
	handOff := func(arg string) {
		go func() {
			detach()
			if err := split.StartWindow(exe, arg); err != nil {
				log.Printf("lanflow: start the window: %v", err)
			}
		}()
	}
	quitAll := func() {
		go func() {
			serviceQuitting.Store(true)
			if client != nil {
				if err := client.QuitService(); err != nil {
					log.Printf("lanflow: quit the service: %v", err)
				}
			}
			quitting.Store(true)
			mygo.App.Quit()
		}()
	}
	askQuitAll := func() {
		withShell(func(s *view.Shell) {
			s.ShowWindow()
			if s.NeedsQuitConfirm() {
				s.AskQuit(quitAll)
				return
			}
			quitAll()
		})
	}
	onUI := func(c rpc.UICommand) {
		switch c.Cmd {
		case rpc.UIShow:
			if leaving() {
				handOff(c.Arg)
				return
			}
			open(c.Arg)
		case rpc.UIToggle:
			if leaving() {
				handOff("")
				return
			}
			withShell(func(s *view.Shell) { s.ToggleWindow() })
		case rpc.UIAskQuit:
			askQuitAll()
		case rpc.UIQuit:
			if restarting.Load() != 0 {
				return // the old service of a restart
			}
			serviceQuitting.Store(true)
			quitting.Store(true)
			mygo.App.Quit()
		}
	}
	startService := func() error { return split.StartService(exe, false) }
	cl, res, err := split.Connect(context.Background(), split.ConnectOptions{
		DataDir: o.DataDir, Version: version, Attach: firstOf(args.Open, args.Link),
		Start: startService, OnUI: onUI,
		Hold: func() bool {
			if serviceQuitting.Load() {
				return true
			}
			if pid := restarting.Load(); pid != 0 {
				if split.Alive(int(pid)) {
					return true
				}
				restarting.Store(0)
			}
			return false
		},
		OnState: func(up bool, err error) {
			if !up && (serviceQuitting.Load() || restarting.Load() != 0) {
				return
			}
			withShell(func(s *view.Shell) { s.ServiceConnection(up) })
		},
	})
	switch {
	case errors.Is(err, split.ErrNotSplit), err == nil && res.Forwarded:
		return // another window shows instead
	case errors.Is(err, split.ErrOldService):
		showError(split.OldServiceText())
		return
	case err != nil:
		// The service would not come up: run everything here instead,
		// rather than show nothing. A service that runs but does not
		// answer keeps the lock: then say so (FallbackFlag keeps it from
		// starting yet another window for us).
		log.Printf("lanflow: no service (%v); running as one process", err)
		os.Args = append(os.Args, split.FallbackFlag)
		if !mygo.App.RequestSingleInstanceLock() {
			showError(split.NoServiceText(err))
			return
		}
		runSingle()
		return
	}
	client = cl
	var detachOnce sync.Once
	detachUI := func() { detachOnce.Do(func() { _ = cl.DetachUI() }) }
	detachFn.Store(&detachUI)
	i18n.SetLang(cl.Settings().Language)
	view.ApplyLanguage(cl.Settings().Language)
	view.ApplyAppearance(cl.Settings().Appearance)
	sh := view.New(cl, view.Options{Mac: runtime.GOOS == "darwin", Remote: true, SetShortcut: cl.SetShortcut})
	if p := os.Getenv("LANFLOW_PAGE"); p != "" && os.Getenv("LANFLOW_FAKE") == "1" {
		page, arg, _ := strings.Cut(p, ":")
		sh.Env().Navigate(env.Page(page), arg)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		signaled.Store(true)
		if !serviceQuitting.Load() {
			go detach() // at once: a new window may be asked for meanwhile
		}
		mygo.App.Quit()
		time.Sleep(quitWatchdog) // the main thread may be stuck: leave anyway
		log.Printf("lanflow: window: quit did not finish in %s; exiting", quitWatchdog)
		os.Exit(0)
	}()
	mygo.App.OnActivate(func(hasVisibleWindows bool) {
		if !hasVisibleWindows {
			withShell(func(s *view.Shell) { s.ShowWindow() })
		}
	})
	mygo.App.OnOpenURL(func(u string) { open(u) })
	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
		// Closing the window ends this process only; any other quit (⌘Q,
		// the app menu, the tray asking) is 退出澜流 and ends the service
		// too, after the usual confirmation.
		if quitting.Load() || closingWindow.Load() || serviceQuitting.Load() || signaled.Load() {
			quitting.Store(true)
			if !serviceQuitting.Load() {
				go detach() // at once: a new window may be asked for meanwhile
			}
			return
		}
		e.PreventDefault()
		askQuitAll()
	})
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		if !serviceQuitting.Load() {
			detach()
		}
		// Saving the UI state goes to the service; wait for it a moment,
		// not for a service that does not answer.
		done := make(chan struct{})
		go func() { sh.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			log.Printf("lanflow: window: saving the UI state timed out")
		}
		_ = cl.Close()
	})
	mygo.App.WhenReady(func() {
		if mygo.App.WasOpenedAtLogin() && cl.Settings().SilentStart {
			// A login item that opens the app (macOS SMAppService, the
			// version before the split) started this window process; the
			// service is up now. Starting silently: nothing to show.
			closingWindow.Store(true)
			mygo.App.Quit()
			return
		}
		win := newMainWindow(sh, false)
		sh.SetCloseWindow(func() {
			closingWindow.Store(true)
			win.Close()
		})
		win.OnClose(func(e *mygo.CloseEvent) {
			if quitting.Load() || serviceQuitting.Load() {
				return
			}
			if !cl.Settings().CloseToTray {
				// 关闭窗口时退出: quit Lanflow, as in the single process.
				e.PreventDefault()
				mygo.App.Quit()
				return
			}
			closingWindow.Store(true)
			go detach() // a new window may attach while this one goes
			sh.HiddenToTray()
		})
		shellMu.Lock()
		shell = sh
		queued := pendingOpen
		pendingOpen = nil
		shellMu.Unlock()
		if err := sh.RegisterGlobalShortcut(); err != nil {
			log.Printf("lanflow: global shortcut: %v", err)
		}
		sh.RegisterShortcuts() // through the service (Options.SetShortcut)
		sh.CheckUpdatesAtLaunch()
		for _, a := range append([]string{args.Open}, queued...) {
			sh.Open(a)
		}
		if res.PID != 0 && cl.Conn() != nil {
			if sv := cl.Conn().Welcome().Version; sv != "" && version != "" && sv != version {
				sh.AskRestartService(sv, version, func() {
					restarting.Store(int64(res.PID))
					if err := cl.QuitService(); err != nil {
						log.Printf("lanflow: restart the service: %v", err)
						restarting.Store(0)
					}
				})
			}
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// quitWatchdog is how long a signalled process waits for the normal quit
// before it exits on its own.
const quitWatchdog = 5 * time.Second

// showError tells the user why the window cannot open, without a window.
func showError(msg string) {
	mygo.App.WhenReady(func() {
		go func() {
			_, _ = mygo.Dialog.Message(mygo.MessageOptions{Type: mygo.MessageWarning, Title: i18n.T("澜流 Lanflow"), Message: msg})
			mygo.App.Quit()
		}()
	})
	_ = mygo.App.Run()
}

func executable() string {
	exe, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	return exe
}

func firstOf(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}
