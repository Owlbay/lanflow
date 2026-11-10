package rpc

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/model"
)

// Answer is the user's reply to a consent request.
type Answer string

const (
	AnswerOnce   Answer = "once"   // allow for this connection
	AnswerAlways Answer = "always" // allow and remember (writes only)
	AnswerDeny   Answer = "deny"   // refuse (remembered for writes)
)

// Consent is what the window asks the user.
type Consent struct {
	Client  string // "ctl" or "mcp:<name>"
	Agent   string // the MCP client's name; "" for ctl
	Kind    Level  // LevelWrite, LevelSensitive or LevelDanger
	Method  string
	Summary string // the call, parameters redacted
}

// PromptFunc asks the user and returns the answer; it returns an error
// when it cannot ask (no window). It may block until the user answers or
// ctx ends.
type PromptFunc func(ctx context.Context, c Consent) (Answer, error)

// consentTimeout bounds how long a call waits for the user.
var consentTimeout = 2 * time.Minute

// guard applies the permission table of docs/service-split.md §4:
//
//	level      ctl                         mcp
//	read       yes                         yes
//	sensitive  --reveal + confirmation     confirmation, per connection
//	write      yes (audited)               first write asks: once/always/deny
//	danger     confirmation every time     no
//	ui         no                          no
type guard struct {
	api    app.API
	prompt PromptFunc
	mu     sync.Mutex // serializes grant updates
}

func denied(format string, args ...any) error {
	return &Error{Code: CodeDenied, Message: fmt.Sprintf(format, args...)}
}

func refused(format string, args ...any) error {
	return &Error{Code: CodeConsent, Message: fmt.Sprintf(format, args...)}
}

func (g *guard) check(ss *session, method string, level Level, summary string) error {
	id := ss.identity()
	if !id.Agent() {
		return nil
	}
	switch level {
	case LevelRead:
		return nil
	case LevelUI:
		return denied("%s 只供澜流界面调用", method)
	case LevelDanger:
		if id.Kind == KindMCP {
			return denied("MCP 不开放危险操作 %s：请在澜流界面中操作", method)
		}
		a, err := g.ask(ss, Consent{Client: id.String(), Kind: LevelDanger, Method: method, Summary: summary})
		if err != nil {
			return err
		}
		if a == AnswerDeny {
			return refused("用户拒绝了 %s", method)
		}
		return nil
	case LevelSensitive:
		return g.checkSensitive(ss, id, method, summary)
	case LevelWrite:
		if id.Kind == KindCtl {
			return nil
		}
		return g.checkWrite(ss, id, method, summary)
	}
	return denied("%s 的权限级别未知", method)
}

func (g *guard) checkSensitive(ss *session, id Identity, method, summary string) error {
	ss.mu.Lock()
	reveal := ss.reveal
	ss.mu.Unlock()
	if id.Kind == KindCtl && !reveal {
		return denied("%s 会返回敏感内容：需要加 --reveal，并在澜流界面中确认", method)
	}
	ss.askMu.Lock()
	defer ss.askMu.Unlock()
	switch ss.sensitive {
	case 1:
		return nil
	case -1:
		return refused("用户已拒绝本次连接读取敏感内容")
	}
	a, err := g.askLocked(ss, Consent{Client: id.String(), Agent: id.Name, Kind: LevelSensitive, Method: method, Summary: summary})
	if err != nil {
		return err
	}
	if a == AnswerDeny {
		ss.sensitive = -1
		return refused("用户拒绝了读取敏感内容（%s）", method)
	}
	ss.sensitive = 1
	return nil
}

func (g *guard) checkWrite(ss *session, id Identity, method, summary string) error {
	if allow, ok := g.grant(id.Name); ok {
		if allow {
			return nil
		}
		return refused("用户已拒绝「%s」修改澜流的配置；可在 设置 › 高级 › Agent 接入 撤销这个决定", id.Name)
	}
	ss.askMu.Lock()
	defer ss.askMu.Unlock()
	if ss.writeOK {
		return nil
	}
	// Another connection of the same agent may have been answered meanwhile.
	if allow, ok := g.grant(id.Name); ok {
		if allow {
			return nil
		}
		return refused("用户已拒绝「%s」修改澜流的配置", id.Name)
	}
	a, err := g.askLocked(ss, Consent{Client: id.String(), Agent: id.Name, Kind: LevelWrite, Method: method, Summary: summary})
	if err != nil {
		return err
	}
	switch a {
	case AnswerOnce:
		ss.writeOK = true
		return nil
	case AnswerAlways:
		if err := g.setGrant(id.Name, true); err != nil {
			return err
		}
		return nil
	default:
		if err := g.setGrant(id.Name, false); err != nil {
			return err
		}
		return refused("用户拒绝了「%s」修改澜流的配置", id.Name)
	}
}

func (g *guard) ask(ss *session, c Consent) (Answer, error) {
	ss.askMu.Lock()
	defer ss.askMu.Unlock()
	return g.askLocked(ss, c)
}

func (g *guard) askLocked(ss *session, c Consent) (Answer, error) {
	if g.prompt == nil {
		return "", refused("需要用户授权，但澜流无法弹出授权窗口（没有界面）")
	}
	ctx, cancel := context.WithTimeout(ss.ctx, consentTimeout)
	defer cancel()
	a, err := g.prompt(ctx, c)
	if err != nil {
		if ctx.Err() != nil {
			return "", refused("等待用户授权超时")
		}
		return "", refused("无法请求用户授权: %v", err)
	}
	return a, nil
}

// grant returns the stored answer for agent.
func (g *guard) grant(agent string) (allow, ok bool) {
	for _, gr := range g.api.Settings().AgentGrants {
		if strings.EqualFold(gr.Agent, agent) {
			return gr.Allow, true
		}
	}
	return false, false
}

func (g *guard) setGrant(agent string, allow bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	s := g.api.Settings()
	out := s.AgentGrants[:0:0]
	for _, gr := range s.AgentGrants {
		if !strings.EqualFold(gr.Agent, agent) {
			out = append(out, gr)
		}
	}
	s.AgentGrants = append(out, model.AgentGrant{Agent: agent, Allow: allow, At: time.Now()})
	if err := g.api.SaveSettings(s); err != nil {
		return fmt.Errorf("无法保存授权结果: %w", err)
	}
	return nil
}
