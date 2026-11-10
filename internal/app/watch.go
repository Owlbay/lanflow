package app

import (
	"context"
	"errors"
	"net"
	"sort"
	"strings"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// Sleep/wake and network changes (docs/design.md §9 「睡眠唤醒、网络切换」):
// after a wake or a network change the service checks that the core still
// answers and that the system proxy it set still points at it, re-reads the
// network information, looks the public IP up again and re-measures the
// main exit. Detection is portable and cheap: a ticker that notices a jump
// of the wall clock (the machine slept, or the process was paused) and a
// fingerprint of the network interfaces (stdlib, no commands) that must
// stay changed for two ticks before it counts, so DHCP churn is ignored.
// main.go additionally feeds the OS's own events in (Resumed,
// NetworkChanged); both paths meet in systemChanged, which debounces.

const (
	watchInterval   = 5 * time.Second  // how often the watcher looks
	wakeGap         = 30 * time.Second // a tick this late (beyond the interval) means a wake
	recheckDebounce = 2 * time.Second  // wake and network change usually come together
	pingTimeout     = 5 * time.Second
)

// sysWatcher turns ticks into wake and network-change callbacks. It is
// not safe for concurrent use; the owner calls tick from one goroutine.
type sysWatcher struct {
	now         func() time.Time
	fingerprint func() string
	interval    time.Duration
	onWake      func()
	onNetChange func()

	last    time.Time
	fp      string
	pending string // a changed fingerprint waiting for its second tick
}

// tick looks at the clock and the network once.
func (w *sysWatcher) tick() {
	now := w.now()
	if !w.last.IsZero() && now.Sub(w.last) > w.interval+wakeGap && w.onWake != nil {
		w.onWake()
	}
	w.last = now

	fp := w.fingerprint()
	switch {
	case fp == w.fp:
		w.pending = ""
	case w.fp == "":
		w.fp = fp // first look
	case fp == w.pending:
		w.fp, w.pending = fp, ""
		if w.onNetChange != nil {
			w.onNetChange()
		}
	default:
		w.pending = fp
	}
}

// ifaceFingerprint summarises the interfaces that are up and their
// addresses, so a new Wi-Fi network, a cable or a VPN shows as a change.
// Tunnel interfaces of the core itself (utun on macOS, wintun on Windows)
// come and go with the core and are left out.
func ifaceFingerprint() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var parts []string
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := strings.ToLower(i.Name)
		if strings.HasPrefix(name, "utun") || strings.HasPrefix(name, "tun") || strings.Contains(name, "wintun") || strings.HasPrefix(name, "mihomo") {
			continue
		}
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		var as []string
		for _, a := range addrs {
			as = append(as, a.String())
		}
		sort.Strings(as)
		parts = append(parts, i.Name+"="+strings.Join(as, ","))
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

// watchSystem runs the watcher until shutdown.
func (s *Service) watchSystem() {
	w := &sysWatcher{
		now:         s.d.now,
		fingerprint: ifaceFingerprint,
		interval:    watchInterval,
		onWake:      func() { s.systemChanged("wake") },
		onNetChange: func() { s.systemChanged("network") },
	}
	w.tick()
	t := time.NewTicker(watchInterval)
	defer t.Stop()
	for {
		select {
		case <-s.rootCtx.Done():
			return
		case <-t.C:
			w.tick()
			s.feat2Tick() // Wi-Fi subscription rules, automatic WebDAV backup (feat2state.go)
		}
	}
}

// Resumed tells the service the system woke up (main.go wires the OS event).
func (s *Service) Resumed() { s.systemChanged("wake") }

// NetworkChanged tells the service the network connection changed.
func (s *Service) NetworkChanged() { s.systemChanged("network") }

// systemChanged schedules one recheck shortly, however many events arrive.
// reason is for the log line only.
func (s *Service) systemChanged(reason string) {
	if s.rootCtx.Err() != nil {
		return
	}
	s.recheckMu.Lock()
	defer s.recheckMu.Unlock()
	if s.recheckTimer != nil {
		s.recheckTimer.Stop()
	}
	s.recheckTimer = s.afterFunc(recheckDebounce, func() {
		s.alog.Infof("%s: rechecking core, system proxy and network", reason)
		s.recheck()
	})
}

// recheck is the work after a wake or a network change. It runs off the
// UI thread and never holds mu across I/O.
func (s *Service) recheck() {
	s.mu.Lock()
	running := s.state.st.Core == model.CoreRunning
	ours := s.state.weSetProxy
	port := s.state.settings.MixedPort
	s.mu.Unlock()

	// 1. The core: gone while asleep, or alive but not answering.
	if running {
		switch {
		case !s.d.mgr.Running():
			s.handleCrash(errors.New(i18n.T("系统唤醒后发现内核已退出")))
		case !s.corePings():
			s.notify(Notice{Kind: "error", Title: i18n.T("内核无响应，正在重新启动"), Detail: i18n.T("系统唤醒或网络切换后内核没有应答，澜流会重新启动它。")})
			if err := s.RestartCore(); err != nil {
				s.notify(Notice{Kind: "error", Title: i18n.T("重新启动内核失败"), Detail: err.Error()})
			}
		}
	}

	// 2. The system proxy we set: still ours? The design asks to check,
	// not to fight another app over it, so a foreign change is reported
	// and our ownership dropped.
	if ours {
		m, _ := s.readMarker()
		if cur, err := s.d.sys.Get(); err == nil && !proxyIsOurs(cur, m, port) {
			s.mu.Lock()
			s.state.weSetProxy = false
			s.state.st.SystemProxy = false
			s.mu.Unlock()
			s.removeMarker()
			s.em.emit(TopicStatus)
			s.notify(Notice{Kind: "error", Title: i18n.T("系统代理已被其他程序修改"), Detail: i18n.T("系统代理不再指向澜流。需要时请在「概览」中重新打开系统代理。")})
		}
	}

	// 3. Network information, public IP and the main exit.
	_ = s.refreshNetInfo()
	s.wifiCheck() // a new Wi-Fi network may name another subscription (wifi.go)
	s.refreshYou()
	s.rebuildRoutes()
	s.scheduleMainProbe()
}

// corePings reports whether the controller answers, trying twice.
func (s *Service) corePings() bool {
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(s.rootCtx, pingTimeout)
		_, err := s.d.ctrl.Version(ctx)
		cancel()
		if err == nil {
			return true
		}
		if s.rootCtx.Err() != nil {
			return true // shutting down: nothing to restart
		}
		time.Sleep(time.Second)
	}
	return false
}
