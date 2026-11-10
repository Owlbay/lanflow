package rpc

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/model"
)

// Undo (`lanflow ctl undo`): before an agent's configuration write the
// server snapshots what the write touches (settings, custom rules,
// overrides, a group's selection, the current subscription) and keeps a
// restore function; Undo runs the newest one. The stack lives in memory
// for the life of the app.

const undoKeep = 20

type undoEntry struct {
	at      time.Time
	client  string
	method  string
	desc    string
	restore func() error
}

type undoStack struct {
	mu      sync.Mutex
	entries []undoEntry
}

func (u *undoStack) push(e undoEntry) {
	e.at = time.Now()
	u.mu.Lock()
	defer u.mu.Unlock()
	u.entries = append(u.entries, e)
	if len(u.entries) > undoKeep {
		u.entries = append([]undoEntry(nil), u.entries[len(u.entries)-undoKeep:]...)
	}
}

func (u *undoStack) pop() (undoEntry, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.entries) == 0 {
		return undoEntry{}, false
	}
	e := u.entries[len(u.entries)-1]
	u.entries = u.entries[:len(u.entries)-1]
	return e, true
}

// UndoResult says what Undo reverted.
type UndoResult struct {
	Method string    `json:"method"`
	Client string    `json:"client"`
	At     time.Time `json:"at"`
	Desc   string    `json:"desc"`
}

var errNothingToUndo = &Error{Code: CodeApp, Message: "没有可以撤销的配置写入（只记录本次运行中 ctl 和 MCP 的写入）"}

func (s *Server) runUndo() (UndoResult, error) {
	e, ok := s.undo.pop()
	if !ok {
		return UndoResult{}, errNothingToUndo
	}
	if err := e.restore(); err != nil {
		return UndoResult{}, fmt.Errorf("撤销 %s 失败: %w", e.method, err)
	}
	return UndoResult{Method: e.method, Client: e.client, At: e.at, Desc: e.desc}, nil
}

// undoPrep finishes a snapshot once the write succeeded, given its
// results; nil restore means there is nothing to undo.
type undoPrep func(results []any) (restore func() error, desc string)

var settingsWrites = map[string]bool{
	"SaveSettings": true, "ApplyDNS": true, "ApplySniffer": true,
	"SetMode": true, "SetMainGroup": true, "SetTUN": true, "PatchSettings": true,
}

var ruleWrites = map[string]bool{"SaveCustomRule": true, "DeleteCustomRule": true, "MoveCustomRule": true}

var overrideWrites = map[string]bool{
	"SaveOverride": true, "SaveOverrideEntry": true, "DeleteOverrideEntry": true,
	"MoveOverrideEntry": true, "UpdateOverrideEntry": true,
}

func argString(args []reflect.Value, i int) string {
	if i < len(args) && args[i].Kind() == reflect.String {
		return args[i].String()
	}
	return ""
}

// prepareUndo snapshots the state method is about to change.
func (s *Server) prepareUndo(method string, args []reflect.Value) undoPrep {
	api := s.o.API
	switch {
	case settingsWrites[method]:
		old := api.Settings()
		return func([]any) (func() error, string) {
			return func() error { return restoreSettings(api, old) }, "恢复之前的设置"
		}
	case ruleWrites[method]:
		old := api.CustomRules()
		return func([]any) (func() error, string) {
			return func() error { return restoreRules(api, old) }, "恢复之前的自定义规则"
		}
	case overrideWrites[method]:
		old := api.OverrideList()
		return func([]any) (func() error, string) {
			return func() error { return restoreOverrides(api, old) }, "恢复之前的覆写"
		}
	case method == "SetRuleDisabled":
		idx := int(args[0].Int())
		rules := api.Rules()
		if idx < 0 || idx >= len(rules) {
			return nil
		}
		prev := rules[idx].Disabled
		return func([]any) (func() error, string) {
			return func() error { return api.SetRuleDisabled(idx, prev) }, fmt.Sprintf("恢复第 %d 条规则的启用状态", idx+1)
		}
	case method == "SetSystemProxy":
		prev := api.Status().SystemProxy
		return func([]any) (func() error, string) {
			return func() error { return api.SetSystemProxy(prev) }, "恢复系统代理开关"
		}
	case method == "SelectProxy":
		group := argString(args, 0)
		prev := ""
		for _, g := range api.Proxies().Groups {
			if g.Name == group {
				prev = g.Now
			}
		}
		if prev == "" {
			return nil
		}
		return func([]any) (func() error, string) {
			return func() error { return api.SelectProxy(group, prev) }, fmt.Sprintf("「%s」改回「%s」", group, prev)
		}
	case method == "UseSubscription":
		prev := api.Status().CurrentSubscription
		if prev == "" {
			return nil
		}
		return func([]any) (func() error, string) {
			return func() error { return api.UseSubscription(prev) }, "改回之前的订阅"
		}
	case method == "EditSubscription":
		in, _ := args[0].Interface().(model.Subscription)
		var prev *model.Subscription
		for _, sub := range api.Subscriptions() {
			if sub.ID == in.ID {
				sub := sub
				prev = &sub
			}
		}
		if prev == nil {
			return nil
		}
		return func([]any) (func() error, string) {
			return func() error { return api.EditSubscription(*prev) }, "恢复订阅「" + prev.Name + "」的设置"
		}
	case method == "ImportSubscriptionURL" || method == "ImportSubscriptionFile":
		prevCur := api.Status().CurrentSubscription
		return func(res []any) (func() error, string) {
			if len(res) == 0 {
				return nil, ""
			}
			sub, ok := res[0].(model.Subscription)
			if !ok || sub.ID == "" {
				return nil, ""
			}
			return func() error {
				if api.Status().CurrentSubscription == sub.ID {
					if prevCur == "" {
						return errors.New("新导入的订阅正在使用，且之前没有订阅可以切回")
					}
					if err := api.UseSubscription(prevCur); err != nil {
						return err
					}
				}
				return api.DeleteSubscription(sub.ID)
			}, "删除刚导入的订阅「" + sub.Name + "」"
		}
	}
	return nil
}

// restoreSettings saves old, keeping what agents never change (UI state,
// the agent grants).
func restoreSettings(api app.API, old model.Settings) error {
	cur := api.Settings()
	old.UI = cur.UI
	old.AgentGrants = cur.AgentGrants
	return api.SaveSettings(old)
}

func sameRule(a, b model.CustomRule) bool {
	return a.Type == b.Type && a.Payload == b.Payload && a.Target == b.Target
}

// restoreRules turns the custom rules back into old with the API's own
// operations: delete what is new, re-add or re-save what changed, then
// move rules into the old order.
func restoreRules(api app.API, old []model.CustomRule) error {
	keep := map[string]bool{}
	for _, r := range old {
		keep[r.ID] = true
	}
	for _, r := range api.CustomRules() {
		if !keep[r.ID] {
			if err := api.DeleteCustomRule(r.ID); err != nil {
				return err
			}
		}
	}
	cur := api.CustomRules()
	byID := map[string]model.CustomRule{}
	for _, r := range cur {
		byID[r.ID] = r
	}
	for i := len(old) - 1; i >= 0; i-- {
		r := old[i]
		c, ok := byID[r.ID]
		if ok && sameRule(c, r) {
			continue
		}
		if !ok {
			r.ID = "" // a deleted rule comes back as a new one
		}
		if _, err := api.SaveCustomRule(r); err != nil {
			return err
		}
	}
	for i, want := range old {
		cur := api.CustomRules()
		if j := indexRule(cur, want, i); j > i {
			if err := api.MoveCustomRule(cur[j].ID, i-j); err != nil {
				return err
			}
		}
	}
	return nil
}

// indexRule finds want in rules from index from on: by ID, else by content
// (a rule re-added under a new ID).
func indexRule(rules []model.CustomRule, want model.CustomRule, from int) int {
	for k := from; k < len(rules); k++ {
		if rules[k].ID == want.ID {
			return k
		}
	}
	for k := from; k < len(rules); k++ {
		if sameRule(rules[k], want) {
			return k
		}
	}
	return -1
}

// restoreOverrides turns the override list back into old.
func restoreOverrides(api app.API, old []model.Override) error {
	keep := map[string]bool{}
	for _, o := range old {
		keep[o.ID] = true
	}
	for _, o := range api.OverrideList() {
		if !keep[o.ID] {
			if err := api.DeleteOverrideEntry(o.ID); err != nil {
				return err
			}
		}
	}
	byID := map[string]model.Override{}
	for _, o := range api.OverrideList() {
		byID[o.ID] = o
	}
	ids := make([]string, len(old))
	for i, o := range old {
		c, ok := byID[o.ID]
		if ok && c.Name == o.Name && c.Enabled == o.Enabled && c.Global == o.Global && c.Source == o.Source && c.URL == o.URL && c.Text == o.Text {
			ids[i] = o.ID
			continue
		}
		if !ok {
			o.ID = "" // deleted entries come back under a new ID
		}
		saved, err := api.SaveOverrideEntry(o)
		if err != nil {
			return err
		}
		ids[i] = saved.ID
	}
	for i, id := range ids {
		cur := api.OverrideList()
		for j, o := range cur {
			if o.ID == id && j != i {
				if err := api.MoveOverrideEntry(id, i-j); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}
