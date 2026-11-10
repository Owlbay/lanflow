package view

import (
	"fmt"
	"log"
	"sync"

	"github.com/egoist/mygo"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/view/env"
	"lanflow/internal/view/hotkey"
)

// keys are the global shortcuts a host registered with the system
// (Settings.GlobalShortcut for the system proxy, Settings.Shortcuts for
// the other actions). A refused combination keeps the previous keys and is
// reported; the settings page asks before it saves (hotkey.Register).
type keys struct {
	h       host
	mu      sync.Mutex
	bound   map[string]string // action → accelerator registered with the system
	syncing bool
}

func newKeys(h host) *keys { return &keys{h: h, bound: map[string]string{}} }

// get returns the accelerator bound to action ("" = none).
func (k *keys) get(action string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.bound[action]
}

func (k *keys) remember(action, acc string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if acc == "" {
		delete(k.bound, action)
	} else {
		k.bound[action] = acc
	}
}

// set is the hotkey.Registrar: it binds acc ("" = none) to action,
// replacing the keys bound before. When the system refuses acc the
// previous keys are registered again and the error says so. Before the
// app runs (tests) it only remembers.
func (k *keys) set(action, acc string) error {
	prev := k.get(action)
	if !k.h.live() {
		k.remember(action, acc)
		return nil
	}
	// The lock only guards the map: mygo's calls wait for the main
	// thread, which may itself be calling here.
	if acc == prev && (acc == "" || mygo.GlobalShortcut.IsRegistered(acc)) {
		return nil
	}
	if prev != "" {
		mygo.GlobalShortcut.Unregister(prev)
	}
	k.remember(action, "")
	if acc == "" {
		return nil
	}
	fn := func() { k.run(action) }
	if err := mygo.GlobalShortcut.Register(acc, fn); err != nil {
		log.Printf("lanflow: shortcut %s %q: %v", action, acc, err)
		kept := i18n.T("该动作的快捷键保持关闭。")
		if action == model.ShortcutSystemProxy {
			kept = i18n.T("全局快捷键保持关闭。")
		}
		if prev != "" && mygo.GlobalShortcut.Register(prev, fn) == nil {
			k.remember(action, prev)
			kept = i18n.Tf("仍使用原来的快捷键 %s。", hotkey.Display(prev, k.h.mac()))
		}
		return fmt.Errorf(i18n.T("无法注册快捷键 %s：可能已被其他应用占用。%s可以换一个组合键再试。"), hotkey.Display(acc, k.h.mac()), kept)
	}
	k.remember(action, acc)
	return nil
}

// sync makes the registered keys match set; the system-proxy action only
// with proxy (the shell follows it on its own).
func (k *keys) sync(set model.Settings, proxy bool) map[string]error {
	k.mu.Lock()
	if k.syncing {
		k.mu.Unlock()
		return nil
	}
	k.syncing = true
	k.mu.Unlock()
	defer func() {
		k.mu.Lock()
		k.syncing = false
		k.mu.Unlock()
	}()
	var errs map[string]error
	for _, action := range model.ShortcutActions {
		if action == model.ShortcutSystemProxy && !proxy {
			continue
		}
		want := set.ShortcutOf(action)
		if want == k.get(action) && (want == "" || !k.h.live() || mygo.GlobalShortcut.IsRegistered(want)) {
			continue
		}
		if err := k.set(action, want); err != nil {
			if errs == nil {
				errs = map[string]error{}
			}
			errs[action] = err
		}
	}
	return errs
}

// run performs action; it is called on the main thread by the key press,
// so the work goes off it (host.runJob) like the tray's commands.
func (k *keys) run(action string) {
	h := k.h
	api := h.hostAPI()
	st := api.Status()
	setMode := func(mode model.Mode) {
		h.runJob(func() error { return api.SetMode(mode) }, i18n.Tf("已切换到%s模式", env.ModeName(mode)))
	}
	switch action {
	case model.ShortcutSystemProxy:
		toggleSystemProxy(h)
	case model.ShortcutTUN:
		h.setTUN(!st.TUN)
	case model.ShortcutCycleMode:
		next := map[model.Mode]model.Mode{model.ModeRule: model.ModeGlobal, model.ModeGlobal: model.ModeDirect, model.ModeDirect: model.ModeRule}[st.Mode]
		if next == "" {
			next = model.ModeRule
		}
		setMode(next)
	case model.ShortcutModeRule:
		setMode(model.ModeRule)
	case model.ShortcutModeGlobal:
		setMode(model.ModeGlobal)
	case model.ShortcutModeDirect:
		setMode(model.ModeDirect)
	case model.ShortcutWindow:
		h.toggleWindow()
	case model.ShortcutTestDelay:
		g, ok := mainGroup(api.Proxies(), st)
		if !ok {
			h.failed(i18n.T("没有可测速的代理组"))
			return
		}
		name := g.Name
		h.runJob(func() error { return api.TestGroupDelay(name) }, i18n.Tf("「%s」测速完成", name))
	}
}
