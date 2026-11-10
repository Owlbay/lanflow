package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// Methods of the local interface that are not in app.API.
//
//   - PatchSettings(patch, dryRun): agents change settings field by field
//     (they read redacted settings, so writing a whole struct back would
//     store masked values); dryRun only returns the diff.
//   - Undo(): reverts the newest configuration write of ctl or MCP.
//   - AuditLog(n): the newest audit entries.
//   - Methods(): the manifest, for compatibility checks and tool lists.
//   - PatchUIState(u): the remote form of UpdateUIState (window only).

type agentOp struct {
	Method
	// level overrides Method.Level for given arguments (PatchSettings
	// with dryRun is a read).
	level func(args []json.RawMessage) Level
	run   func(s *Server, ss *session, args []json.RawMessage) (any, error)
}

var agentOps = map[string]agentOp{
	"PatchSettings": {
		Method: Method{Name: "PatchSettings", Agent: true, Level: LevelWrite, Error: true,
			Params:  []Param{{Name: "patch", Type: "map[string]any"}, {Name: "dryRun", Type: "bool"}},
			Results: []string{"rpc.PatchResult"},
			Doc:     "PatchSettings merges patch (a JSON object of settings fields, nested like Settings, e.g. {\"Mode\":\"rule\",\"DNS\":{\"TakeOver\":true}}) into the settings and applies them; dryRun returns the diff without saving."},
		level: func(args []json.RawMessage) Level {
			var dry bool
			if len(args) > 1 && json.Unmarshal(args[1], &dry) == nil && dry {
				return LevelRead
			}
			return LevelWrite
		},
		run: func(s *Server, ss *session, args []json.RawMessage) (any, error) {
			var dry bool
			if err := json.Unmarshal(args[1], &dry); err != nil {
				return nil, &Error{Code: CodeInvalidParams, Message: "dryRun 应为 true 或 false"}
			}
			return s.patchSettings(ss, args[0], dry)
		},
	},
	"Undo": {
		Method: Method{Name: "Undo", Agent: true, Level: LevelWrite, Error: true, Results: []string{"rpc.UndoResult"},
			Doc: "Undo reverts the newest configuration write made through ctl or MCP in this run of Lanflow (settings, custom rules, overrides, proxy selection, current subscription)."},
		run: func(s *Server, ss *session, _ []json.RawMessage) (any, error) {
			res, err := s.runUndo()
			if res.Method != "" || err != errNothingToUndo {
				s.audit.addSummary(ss.identity().String(), "Undo", res.Method, err)
			}
			if err != nil {
				return nil, err
			}
			return []any{res}, nil
		},
	},
	"AuditLog": {
		Method: Method{Name: "AuditLog", Agent: true, Level: LevelRead,
			Params: []Param{{Name: "n", Type: "int"}}, Results: []string{"[]rpc.AuditEntry"},
			Doc: "AuditLog returns the newest n entries of the audit log (writes and refused calls of ctl and MCP), oldest first."},
		run: func(s *Server, _ *session, args []json.RawMessage) (any, error) {
			var n int
			_ = json.Unmarshal(args[0], &n)
			return []any{s.audit.recent(n)}, nil
		},
	},
	"PatchUIState": {
		Method: Method{Name: "PatchUIState", Agent: true, Level: LevelUI, Error: true,
			Params: []Param{{Name: "u", Type: "model.UIState"}},
			Doc:    "PatchUIState replaces the remembered UI state (the remote form of UpdateUIState)."},
		run: func(s *Server, _ *session, args []json.RawMessage) (any, error) {
			var u model.UIState
			if err := json.Unmarshal(args[0], &u); err != nil {
				return nil, &Error{Code: CodeInvalidParams, Message: err.Error()}
			}
			return []any{}, s.o.API.UpdateUIState(func(p *model.UIState) { *p = u })
		},
	},
}

// Methods is registered in init: its result is the manifest, which lists
// agentOps itself.
func init() {
	agentOps["Methods"] = agentOp{
		Method: Method{Name: "Methods", Agent: true, Level: LevelRead, Results: []string{"[]rpc.Method"},
			Doc: "Methods returns the manifest of the local interface: every method with its parameters and permission level."},
		run: func(*Server, *session, []json.RawMessage) (any, error) { return []any{Manifest()}, nil },
	}
}

func (s *Server) callAgentOp(ss *session, name string, op agentOp, params json.RawMessage) (any, error) {
	var args []json.RawMessage
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &args); err != nil {
			return nil, &Error{Code: CodeInvalidParams, Message: "参数应为数组"}
		}
	}
	if len(args) != len(op.Params) {
		return nil, &Error{Code: CodeInvalidParams, Message: fmt.Sprintf("需要 %d 个参数，收到 %d 个", len(op.Params), len(args))}
	}
	level := op.Level
	if op.level != nil {
		level = op.level(args)
	}
	id := ss.identity()
	if id.Agent() {
		vals := make([]any, len(args))
		for i, a := range args {
			vals[i] = json.RawMessage(a)
		}
		if err := s.guard.check(ss, name, level, summarizeAny(name, vals)); err != nil {
			if level != LevelRead {
				s.audit.addSummary(id.String(), name, summarizeAny(name, vals), err)
			}
			return nil, err
		}
	}
	return op.run(s, ss, args)
}

// PatchResult is what PatchSettings returns.
type PatchResult struct {
	Changed []string         `json:"changed"` // top-level settings fields that change
	Diff    []model.DiffLine `json:"diff"`    // redacted settings, before → after, with context
	Applied bool             `json:"applied"`
}

// patchLocked lists the settings agents may not patch: the window's own
// state, the grants, the core binary and the backup credentials.
var patchLocked = map[string]bool{
	"UI": true, "AgentGrants": true, "ConnHiddenColumns": true,
	"CorePath": true, "CoreVersion": true, "WebDAV": true,
}

func (s *Server) patchSettings(ss *session, patch json.RawMessage, dry bool) (any, error) {
	cur := s.o.API.Settings()
	next, changed, err := applyPatch(cur, patch, ss.identity().Agent())
	if err != nil {
		return nil, &Error{Code: CodeInvalidParams, Message: err.Error()}
	}
	res := PatchResult{Changed: changed, Diff: SettingsDiff(cur, next)}
	if dry || len(changed) == 0 {
		return []any{res}, nil
	}
	id := ss.identity()
	prep := s.prepareUndo("PatchSettings", nil)
	err = s.o.API.SaveSettings(next)
	s.audit.addSummary(id.String(), "PatchSettings", truncate("PatchSettings("+strings.Join(changed, ", ")+")", 300), err)
	if err != nil {
		return nil, err
	}
	if restore, desc := prep(nil); restore != nil {
		s.undo.push(undoEntry{client: id.String(), method: "PatchSettings", desc: desc + "（" + strings.Join(changed, ", ") + "）", restore: restore})
	}
	res.Applied = true
	return []any{res}, nil
}

// applyPatch merges patch into cur. Keys match the Settings field names
// case-insensitively; unknown keys are errors. With agent, locked fields
// are refused.
func applyPatch(cur model.Settings, patch json.RawMessage, agent bool) (model.Settings, []string, error) {
	var p map[string]any
	dec := json.NewDecoder(bytes.NewReader(patch))
	dec.UseNumber()
	if err := dec.Decode(&p); err != nil || p == nil {
		return cur, nil, fmt.Errorf("补丁应为 JSON 对象，例如 {\"Mode\":\"global\"}")
	}
	b, _ := json.Marshal(cur)
	var base map[string]any
	dec = json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	_ = dec.Decode(&base)
	t := reflect.TypeOf(cur)
	for k := range p {
		if f, ok := fieldFold(t, k); ok && agent && patchLocked[f.Name] {
			return cur, nil, fmt.Errorf("设置项 %s 不能经 agent 修改", f.Name)
		}
	}
	if err := mergeInto(base, p, t, ""); err != nil {
		return cur, nil, err
	}
	// Only the patched fields are decoded, so the others stay exactly as
	// they were (times keep their location, nil slices stay nil).
	next := cur
	nv := reflect.ValueOf(&next).Elem()
	var changed []string
	for k := range p {
		f, _ := fieldFold(t, k)
		b, _ := json.Marshal(base[f.Name])
		fv := reflect.New(f.Type)
		fv.Elem().Set(nv.FieldByName(f.Name))
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		if err := dec.Decode(fv.Interface()); err != nil {
			return cur, nil, fmt.Errorf("设置项 %s 的值无效: %v", f.Name, err)
		}
		old, _ := json.Marshal(nv.FieldByName(f.Name).Interface())
		now, _ := json.Marshal(fv.Elem().Interface())
		nv.FieldByName(f.Name).Set(fv.Elem())
		if !bytes.Equal(old, now) {
			changed = append(changed, f.Name)
		}
	}
	sort.Strings(changed)
	return next, changed, nil
}

func fieldFold(t reflect.Type, name string) (reflect.StructField, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return reflect.StructField{}, false
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.IsExported() && strings.EqualFold(f.Name, name) {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

func mergeInto(dst map[string]any, patch map[string]any, t reflect.Type, path string) error {
	for k, v := range patch {
		f, ok := fieldFold(t, k)
		if !ok {
			return fmt.Errorf("没有设置项 %s%s", path, k)
		}
		ft := f.Type
		if pv, ok := v.(map[string]any); ok && ft.Kind() == reflect.Struct {
			dv, _ := dst[f.Name].(map[string]any)
			if dv == nil {
				dv = map[string]any{}
			}
			if err := mergeInto(dv, pv, ft, path+f.Name+"."); err != nil {
				return err
			}
			dst[f.Name] = dv
			continue
		}
		dst[f.Name] = v
	}
	return nil
}

// SettingsDiff is the redacted line diff of two settings, as indented JSON
// with two lines of context around changes.
func SettingsDiff(before, after model.Settings) []model.DiffLine {
	a, _ := json.MarshalIndent(RedactValue(before), "", "  ")
	b, _ := json.MarshalIndent(RedactValue(after), "", "  ")
	return CompactDiff(profile.Diff(a, b), 2)
}

// CompactDiff keeps the changed lines and ctx lines around them; skipped
// runs become one "…" line.
func CompactDiff(lines []model.DiffLine, ctx int) []model.DiffLine {
	keep := make([]bool, len(lines))
	for i, l := range lines {
		if l.Op == '+' || l.Op == '-' {
			for j := max(0, i-ctx); j <= min(len(lines)-1, i+ctx); j++ {
				keep[j] = true
			}
		}
	}
	var out []model.DiffLine
	skipped := false
	for i, l := range lines {
		if !keep[i] {
			skipped = true
			continue
		}
		if skipped && len(out) > 0 {
			out = append(out, model.DiffLine{Op: ' ', Text: "…"})
		}
		skipped = false
		out = append(out, l)
	}
	return out
}
