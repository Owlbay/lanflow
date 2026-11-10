package rpc

import (
	"encoding/json"
	"log"

	"lanflow/internal/app"
)

// Window ops: the methods the window process uses to live beside the
// service process (docs/service-split.md S2). All are LevelUI, so no
// agent can call them.
//
//   - AttachUI(arg): the window process announces itself. When another
//     window process is attached (or the serving process has the window
//     itself), that one is asked to show (UICommand UIShow with arg) and
//     the caller is told to exit (Forwarded); otherwise the caller becomes
//     the window and the service leaves its background pace.
//   - DetachUI(): the window is closing; a later AttachUI takes over.
//   - QuitService(): 退出澜流, confirmed in the window: the serving process
//     quits (restoring the system proxy) and tells the window to quit.
//   - SetBackground(hidden): the window's visibility (app.Service paces
//     its streams by it).
//   - SetShortcut(action, acc): registers a global shortcut in the serving
//     process, which holds them (hotkey.Registrar of the settings page).
//   - CacheUsage(), CleanCaches(): the optional cache API (设置 › 数据).

// Host is what the window ops need of the serving process.
type Host interface {
	// Split reports whether the window runs in its own process. False is
	// the single process: AttachUI then always forwards to ShowWindow.
	Split() bool
	// ShowWindow brings up the window of the single process (Split false).
	ShowWindow(arg string)
	SetBackground(hidden bool)
	Quit()
	SetShortcut(action, acc string) error
}

// AttachResult is what AttachUI returns.
type AttachResult struct {
	// Forwarded: a window already exists and was asked to show; the
	// caller should exit.
	Forwarded bool `json:"forwarded"`
	PID       int  `json:"pid"`
	// Split is false when the serving process is the single process (an
	// older or single-process Lanflow with its own window).
	Split bool `json:"split"`
}

type cacheAPI interface {
	CacheUsage() []app.CacheItem
	CleanCaches() (freed int64, err error)
}

func windowOp(name, doc string, params []Param, results []string, hasErr bool, run func(s *Server, ss *session, args []json.RawMessage) (any, error)) {
	agentOps[name] = agentOp{
		Method: Method{Name: name, Agent: true, Level: LevelUI, Params: params, Results: results, Error: hasErr, Doc: doc},
		run: func(s *Server, ss *session, args []json.RawMessage) (any, error) {
			if ss.identity().Kind != KindUI {
				return nil, denied("%s 只供澜流界面调用", name)
			}
			if s.o.Host == nil && name != "CacheUsage" && name != "CleanCaches" {
				return nil, &Error{Code: CodeNoMethod, Message: "这个澜流进程不提供 " + name}
			}
			return run(s, ss, args)
		},
	}
}

func init() {
	windowOp("AttachUI", "AttachUI makes the caller the window process, or asks the window that exists to show arg (Forwarded).",
		[]Param{{Name: "arg", Type: "string"}}, []string{"rpc.AttachResult"}, false,
		func(s *Server, ss *session, args []json.RawMessage) (any, error) {
			var arg string
			_ = json.Unmarshal(args[0], &arg)
			return []any{s.attachUI(ss, arg)}, nil
		})
	windowOp("DetachUI", "DetachUI tells the service that the window is closing.", nil, nil, false,
		func(s *Server, ss *session, _ []json.RawMessage) (any, error) {
			s.detachUI(ss)
			return []any{}, nil
		})
	windowOp("QuitService", "QuitService quits Lanflow (the service process), as confirmed in the window.", nil, nil, false,
		func(s *Server, _ *session, _ []json.RawMessage) (any, error) {
			go s.o.Host.Quit() // after the reply
			return []any{}, nil
		})
	windowOp("SetBackground", "SetBackground passes the window's visibility to the service.",
		[]Param{{Name: "hidden", Type: "bool"}}, nil, false,
		func(s *Server, _ *session, args []json.RawMessage) (any, error) {
			var hidden bool
			_ = json.Unmarshal(args[0], &hidden)
			s.o.Host.SetBackground(hidden)
			return []any{}, nil
		})
	windowOp("SetShortcut", "SetShortcut registers acc (\"\" = none) as the global shortcut of action in the service process.",
		[]Param{{Name: "action", Type: "string"}, {Name: "acc", Type: "string"}}, nil, true,
		func(s *Server, _ *session, args []json.RawMessage) (any, error) {
			var action, acc string
			_ = json.Unmarshal(args[0], &action)
			_ = json.Unmarshal(args[1], &acc)
			return []any{}, s.o.Host.SetShortcut(action, acc)
		})
	windowOp("CacheUsage", "CacheUsage reports the caches on disk (设置 › 数据); empty when the service has none.",
		nil, []string{"[]app.CacheItem"}, false,
		func(s *Server, _ *session, _ []json.RawMessage) (any, error) {
			c, ok := s.o.API.(cacheAPI)
			if !ok {
				return []any{[]app.CacheItem{}}, nil
			}
			return []any{c.CacheUsage()}, nil
		})
	windowOp("CleanCaches", "CleanCaches empties the caches and returns the bytes freed.",
		nil, []string{"int64"}, true,
		func(s *Server, _ *session, _ []json.RawMessage) (any, error) {
			c, ok := s.o.API.(cacheAPI)
			if !ok {
				return []any{int64(0)}, nil
			}
			n, err := c.CleanCaches()
			return []any{n}, err
		})
}

func (s *Server) attachUI(ss *session, arg string) AttachResult {
	res := AttachResult{PID: pid(), Split: s.o.Host.Split()}
	if !res.Split {
		s.o.Host.ShowWindow(arg)
		res.Forwarded = true
		return res
	}
	s.mu.Lock()
	cur := s.ui
	if cur != nil && cur != ss && cur.ctx.Err() == nil {
		s.mu.Unlock()
		cur.sendUI(UICommand{Cmd: UIShow, Arg: arg})
		res.Forwarded = true
		return res
	}
	s.ui = ss
	s.mu.Unlock()
	s.o.Host.SetBackground(false)
	return res
}

// detachUI forgets ss as the window process; the service goes back to
// its background pace.
func (s *Server) detachUI(ss *session) {
	s.mu.Lock()
	was := s.ui == ss
	if was {
		s.ui = nil
	}
	s.mu.Unlock()
	if was && s.o.Host != nil {
		s.o.Host.SetBackground(true)
	}
}

// UIAttached reports whether a window process is attached.
func (s *Server) UIAttached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ui != nil
}

// SendUI sends a command to the attached window process; false when none.
func (s *Server) SendUI(cmd, arg string) bool {
	s.mu.Lock()
	cur := s.ui
	s.mu.Unlock()
	if cur == nil {
		return false
	}
	cur.sendUI(UICommand{Cmd: cmd, Arg: arg})
	return true
}

func (ss *session) sendUI(u UICommand) {
	b, err := json.Marshal(u)
	if err != nil {
		log.Printf("lanflow: local api: ui command: %v", err)
		return
	}
	ss.send(message{Method: "ui", Params: b})
}
