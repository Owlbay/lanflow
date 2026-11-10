package profile

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ---- yaml.Node helpers ----

// deref follows alias nodes.
func deref(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

// mapGet returns the value node for key in mapping m (nil if absent). It
// also looks through `<<` merge keys so that names defined via anchors are
// found when reading.
func mapGet(m *yaml.Node, key string) *yaml.Node {
	m = deref(m)
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key && m.Content[i].Tag != "!!merge" {
			return m.Content[i+1]
		}
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if k.Tag != "!!merge" && k.Value != "<<" {
			continue
		}
		v := deref(m.Content[i+1])
		if v == nil {
			continue
		}
		if v.Kind == yaml.MappingNode {
			if r := mapGet(v, key); r != nil {
				return r
			}
		} else if v.Kind == yaml.SequenceNode {
			for _, item := range v.Content {
				if r := mapGet(deref(item), key); r != nil {
					return r
				}
			}
		}
	}
	return nil
}

// mapIndex returns the position of key's key node in m.Content, -1 if absent.
func mapIndex(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key && m.Content[i].Tag != "!!merge" {
			return i
		}
	}
	return -1
}

// mapSet sets key to v in mapping m, keeping the key's position if present
// and appending otherwise.
func mapSet(m *yaml.Node, key string, v *yaml.Node) {
	if i := mapIndex(m, key); i >= 0 {
		m.Content[i+1] = v
		return
	}
	m.Content = append(m.Content, strNode(key), v)
}

// mapDelete removes key from m and reports whether it was present.
func mapDelete(m *yaml.Node, key string) bool {
	if i := mapIndex(m, key); i >= 0 {
		m.Content = append(m.Content[:i:i], m.Content[i+2:]...)
		return true
	}
	return false
}

func strNode(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

func boolNode(b bool) *yaml.Node {
	v := "false"
	if b {
		v = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: v}
}

func intNode(i int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(i)}
}

func seqNode(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
}

func strSeq(items []string) *yaml.Node {
	n := seqNode()
	for _, s := range items {
		n.Content = append(n.Content, strNode(s))
	}
	return n
}

// mapNode builds a mapping from alternating key, value arguments.
func mapNode(kv ...any) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i+1 < len(kv); i += 2 {
		n.Content = append(n.Content, strNode(kv[i].(string)), kv[i+1].(*yaml.Node))
	}
	return n
}

// stripComments removes comments from n and its descendants so that
// override comments do not leak into the composed config.
func stripComments(n *yaml.Node) {
	if n == nil {
		return
	}
	n.HeadComment, n.LineComment, n.FootComment = "", "", ""
	for _, c := range n.Content {
		stripComments(c)
	}
}

// walk calls fn for n and every descendant (aliases are not followed).
func walk(n *yaml.Node, fn func(*yaml.Node)) {
	if n == nil {
		return
	}
	fn(n)
	for _, c := range n.Content {
		walk(c, fn)
	}
}

// ---- override merge ----

// overrideTracker is notified about top-level keys an override touches
// and about the nodes it introduces. compose uses it to attribute output
// lines to layers; ApplyOverride passes nil.
type overrideTracker interface {
	// touched reports a top-level key the override set or merged into;
	// replaced is false for list prepend/append, whose items are
	// attributed individually.
	touched(topKey string, replaced bool)
}

// mergeOverride merges the override mapping over into base (both mappings)
// following the ApplyOverride semantics. tracker may be nil.
func mergeOverride(base, over *yaml.Node, tracker overrideTracker) error {
	return mergeMapping(base, over, "", tracker)
}

func lineErr(n *yaml.Node, format string, args ...any) error {
	return fmt.Errorf("第 %d 行：%s", n.Line, fmt.Sprintf(format, args...))
}

func kindName(n *yaml.Node) string {
	switch deref(n).Kind {
	case yaml.MappingNode:
		return "映射"
	case yaml.SequenceNode:
		return "列表"
	case yaml.ScalarNode:
		return "标量"
	}
	return "未知类型"
}

func mergeMapping(base, over *yaml.Node, path string, tracker overrideTracker) error {
	for i := 0; i+1 < len(over.Content); i += 2 {
		k, v := over.Content[i], over.Content[i+1]
		if k.Kind != yaml.ScalarNode {
			return lineErr(k, "覆写的键必须是字符串")
		}
		raw := k.Value
		name, mode := raw, "set"
		switch {
		case strings.HasPrefix(raw, "+") && len(raw) > 1:
			name, mode = raw[1:], "prepend"
		case strings.HasSuffix(raw, "+") && len(raw) > 1:
			name, mode = raw[:len(raw)-1], "append"
		case strings.HasSuffix(raw, "!") && len(raw) > 1:
			name, mode = raw[:len(raw)-1], "replace"
		}
		full := name
		if path != "" {
			full = path + "." + name
		}
		if path == "" && tracker != nil {
			tracker.touched(name, mode != "prepend" && mode != "append")
		}
		switch mode {
		case "prepend", "append":
			vv := deref(v)
			if vv.Kind != yaml.SequenceNode {
				return lineErr(k, "`%s` 的值必须是列表（用于向 %s 列表%s）", raw, full, map[string]string{"prepend": "前插入", "append": "后追加"}[mode])
			}
			cur := mapGet(base, name)
			if cur == nil {
				mapSet(base, name, seqNode(append([]*yaml.Node(nil), vv.Content...)...))
				continue
			}
			cur = deref(cur)
			if cur.Kind != yaml.SequenceNode {
				return lineErr(k, "`%s` 要求 %s 是列表，但当前是%s", raw, full, kindName(cur))
			}
			if mapIndex(base, name) < 0 {
				// The list is shared through an alias or merge key: copy it so
				// the anchor's other users are not modified.
				cur = cloneNode(cur)
				cur.Anchor = ""
				mapSet(base, name, cur)
			}
			if mode == "prepend" {
				cur.Content = append(append([]*yaml.Node(nil), vv.Content...), cur.Content...)
			} else {
				cur.Content = append(cur.Content, vv.Content...)
			}
		case "replace":
			mapSet(base, name, v)
		default:
			cur := mapGet(base, name)
			if cur != nil && deref(cur).Kind == yaml.MappingNode && deref(v).Kind == yaml.MappingNode {
				cur = deref(cur)
				if mapIndex(base, name) < 0 || cur != base.Content[mapIndex(base, name)+1] {
					// Shared through an alias or merge key: merge into a copy.
					cur = cloneNode(cur)
					cur.Anchor = ""
					mapSet(base, name, cur)
				}
				if err := mergeMapping(cur, deref(v), full, nil); err != nil {
					return err
				}
				continue
			}
			mapSet(base, name, v)
		}
	}
	return nil
}

// parseOverride parses override text into a mapping node; empty or
// comment-only text yields nil.
func parseOverride(text string) (*yaml.Node, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, errors.New(yamlError(err))
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return nil, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, lineErr(root, "覆写必须是键值映射（key: value），而不是%s", kindName(root))
	}
	stripComments(root)
	return root, nil
}

func encodeDoc(root *yaml.Node) ([]byte, error) {
	// yaml.v3 re-emits explicitly tagged merge keys as `!!merge <<`; drop the
	// tag while encoding so they come out as a plain `<<`.
	var merges []*yaml.Node
	walk(root, func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode && n.Tag == "!!merge" {
			n.Tag = ""
			merges = append(merges, n)
		}
	})
	defer func() {
		for _, n := range merges {
			n.Tag = "!!merge"
		}
	}()
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, fmt.Errorf("生成 YAML 失败：%w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("生成 YAML 失败：%w", err)
	}
	return buf.Bytes(), nil
}

func applyOverride(base []byte, override string) ([]byte, error) {
	over, err := parseOverride(override)
	if err != nil {
		return nil, fmt.Errorf("覆写%s", err.Error())
	}
	if over == nil {
		return base, nil
	}
	root, err := parseDoc(base)
	if err != nil {
		return nil, fmt.Errorf("基础配置%s", err.Error())
	}
	if err := mergeOverride(root, over, nil); err != nil {
		return nil, fmt.Errorf("覆写%s", err.Error())
	}
	return encodeDoc(root)
}
