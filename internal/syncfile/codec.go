package syncfile

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"

	"lanflow/internal/i18n"
)

// The codec maps Go structs to YAML by reflection, so lanflow.yaml, its
// JSON Schema (schema.go) and model.Settings cannot drift apart: a field
// added to model.Settings appears in all three at once, unless it is
// listed in Excluded.
//
// Key names: a `sync:"name"` tag, else the name of a `json:"name"` tag,
// else the Go name in lower camel case (TUNStack → tunStack, DNS → dns).
// `sync:",omitempty"` leaves a zero value out when writing; every other
// field is always written, so the file is explicit and a missing key on
// reading means the default value.

// field is one encoded struct field.
type field struct {
	index     int
	name      string // YAML key
	path      string // dotted YAML path from the document root, "[]" for list items
	omitEmpty bool
}

// fieldNameOverrides fixes the names the camel-case rule gets wrong.
var fieldNameOverrides = map[string]string{"IPv6": "ipv6"}

// lowerCamel turns a Go field name into a YAML key: the leading capitals
// become lower case, except the last of a run followed by a lower-case
// letter (TUNStack → tunStack, ATrust → aTrust, HTTPPorts → httpPorts).
func lowerCamel(s string) string {
	if v, ok := fieldNameOverrides[s]; ok {
		return v
	}
	r := []rune(s)
	n := 0
	for n < len(r) && unicode.IsUpper(r[n]) {
		n++
	}
	switch {
	case n == 0:
		return s
	case n == 1 || n == len(r):
		// "Mode" → "mode", "DNS" → "dns"
	case unicode.IsLower(r[n]):
		n-- // keep the capital that starts the next word
	}
	for i := 0; i < n; i++ {
		r[i] = unicode.ToLower(r[i])
	}
	return string(r)
}

// keyOf returns the YAML key of f; skip for fields never encoded.
func keyOf(f reflect.StructField) (name string, omitEmpty, skip bool) {
	if !f.IsExported() {
		return "", false, true
	}
	if tag, ok := f.Tag.Lookup("sync"); ok {
		if tag == "-" {
			return "", false, true
		}
		n, opts, _ := strings.Cut(tag, ",")
		if n == "" {
			n = lowerCamel(f.Name)
		}
		return n, opts == "omitempty", false
	}
	if tag, ok := f.Tag.Lookup("json"); ok {
		if tag == "-" {
			return "", false, true
		}
		if n, _, _ := strings.Cut(tag, ","); n != "" {
			return n, false, false
		}
	}
	return lowerCamel(f.Name), false, false
}

// fieldsOf lists the encoded fields of struct type t at path.
func fieldsOf(t reflect.Type, path string) []field {
	var out []field
	for i := 0; i < t.NumField(); i++ {
		name, omit, skip := keyOf(t.Field(i))
		if skip {
			continue
		}
		p := join(path, name)
		if isExcluded(p) {
			continue
		}
		out = append(out, field{index: i, name: name, path: p, omitEmpty: omit})
	}
	return out
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

var timeType = reflect.TypeOf(time.Time{})

// ---- encoding ----

// encodeValue returns the YAML node of v at path.
func encodeValue(v reflect.Value, path string) *yaml.Node {
	if v.Type() == timeType {
		t := v.Interface().(time.Time)
		if t.IsZero() {
			return scalar("!!str", "")
		}
		return scalar("!!timestamp", t.UTC().Format(time.RFC3339Nano))
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return scalar("!!null", "null")
		}
		return encodeValue(v.Elem(), path)
	case reflect.Struct:
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, f := range fieldsOf(v.Type(), path) {
			fv := v.Field(f.index)
			if f.omitEmpty && fv.IsZero() {
				continue
			}
			m.Content = append(m.Content, scalar("!!str", f.name), encodeValue(fv, f.path))
		}
		return m
	case reflect.Slice, reflect.Array:
		s := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		if v.Len() == 0 {
			s.Style = yaml.FlowStyle
		}
		for i := 0; i < v.Len(); i++ {
			s.Content = append(s.Content, encodeValue(v.Index(i), path+"[]"))
		}
		return s
	case reflect.Map:
		m := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if v.Len() == 0 {
			m.Style = yaml.FlowStyle
		}
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
		for _, k := range keys {
			m.Content = append(m.Content, scalar("!!str", k.String()), encodeValue(v.MapIndex(k), path+"{}"))
		}
		return m
	case reflect.String:
		n := scalar("!!str", v.String())
		if strings.Contains(v.String(), "\n") {
			n.Style = yaml.LiteralStyle
		}
		return n
	case reflect.Bool:
		return scalar("!!bool", strconv.FormatBool(v.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return scalar("!!int", strconv.FormatInt(v.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return scalar("!!int", strconv.FormatUint(v.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		return scalar("!!float", strconv.FormatFloat(v.Float(), 'g', -1, 64))
	}
	panic(fmt.Sprintf("syncfile: cannot encode %s at %s", v.Type(), path))
}

func scalar(tag, value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}

// ---- decoding ----

// Error is a problem in the file, with where it is.
type Error struct {
	Line   int    // 1-based, 0 when unknown
	Column int    // 1-based, 0 when unknown
	Path   string // YAML path, e.g. settings.dns.nameserver
	Msg    string
}

func (e *Error) Error() string {
	where := ""
	switch {
	case e.Line > 0 && e.Path != "":
		where = i18n.Tf("第 %d 行（%s）：", e.Line, e.Path)
	case e.Line > 0:
		where = i18n.Tf("第 %d 行：", e.Line)
	case e.Path != "":
		where = e.Path + i18n.T("：")
	}
	return where + e.Msg
}

func errAt(n *yaml.Node, path, msg string) *Error {
	e := &Error{Path: path, Msg: msg}
	if n != nil {
		e.Line, e.Column = n.Line, n.Column
	}
	return e
}

// decoder collects the warnings of one decode.
type decoder struct {
	warnings []*Error
}

// decodeValue fills v (settable) from n. v keeps its value for keys the
// file does not have, so the caller presets defaults. Unknown keys are
// warnings, not errors: a newer version on another machine may write
// settings this one does not know yet.
func (d *decoder) decodeValue(n *yaml.Node, v reflect.Value, path string) error {
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	null := n.Kind == yaml.ScalarNode && n.Tag == "!!null"
	if v.Type() == timeType {
		if null || (n.Kind == yaml.ScalarNode && n.Value == "") {
			v.Set(reflect.ValueOf(time.Time{}))
			return nil
		}
		if n.Kind != yaml.ScalarNode {
			return errAt(n, path, i18n.T("应为时间（如 2026-01-02T15:04:05Z）"))
		}
		t, err := time.Parse(time.RFC3339Nano, n.Value)
		if err != nil {
			return errAt(n, path, i18n.T("应为时间（如 2026-01-02T15:04:05Z）"))
		}
		v.Set(reflect.ValueOf(t))
		return nil
	}
	switch v.Kind() {
	case reflect.Pointer:
		if null {
			v.Set(reflect.Zero(v.Type()))
			return nil
		}
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		return d.decodeValue(n, v.Elem(), path)
	case reflect.Struct:
		if null {
			return nil
		}
		if n.Kind != yaml.MappingNode {
			return errAt(n, path, i18n.T("应为键值对象"))
		}
		fields := fieldsOf(v.Type(), path)
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, val := n.Content[i], n.Content[i+1]
			if seen[k.Value] {
				return errAt(k, join(path, k.Value), i18n.T("重复的键"))
			}
			seen[k.Value] = true
			var f *field
			for j := range fields {
				if fields[j].name == k.Value {
					f = &fields[j]
					break
				}
			}
			if f == nil {
				if isExcluded(join(path, k.Value)) {
					continue // a machine-local item: each machine keeps its own
				}
				d.warnings = append(d.warnings, errAt(k, join(path, k.Value), i18n.T("未知的键，已忽略")))
				continue
			}
			if err := d.decodeValue(val, v.Field(f.index), f.path); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice:
		if null {
			v.Set(reflect.Zero(v.Type()))
			return nil
		}
		if n.Kind != yaml.SequenceNode {
			return errAt(n, path, i18n.T("应为列表"))
		}
		out := reflect.MakeSlice(v.Type(), len(n.Content), len(n.Content))
		for i, item := range n.Content {
			if err := d.decodeValue(item, out.Index(i), path+"["+strconv.Itoa(i)+"]"); err != nil {
				return err
			}
		}
		v.Set(out)
		return nil
	case reflect.Map:
		if null {
			v.Set(reflect.Zero(v.Type()))
			return nil
		}
		if n.Kind != yaml.MappingNode {
			return errAt(n, path, i18n.T("应为键值对象"))
		}
		out := reflect.MakeMapWithSize(v.Type(), len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			ev := reflect.New(v.Type().Elem()).Elem()
			if err := d.decodeValue(n.Content[i+1], ev, join(path, k.Value)); err != nil {
				return err
			}
			out.SetMapIndex(reflect.ValueOf(k.Value).Convert(v.Type().Key()), ev)
		}
		v.Set(out)
		return nil
	}
	if n.Kind != yaml.ScalarNode {
		return errAt(n, path, i18n.T("应为单个值"))
	}
	switch v.Kind() {
	case reflect.String:
		if null {
			v.SetString("")
			return nil
		}
		v.SetString(n.Value)
		return nil
	case reflect.Bool:
		if n.Tag != "!!bool" {
			return errAt(n, path, i18n.T("应为 true 或 false"))
		}
		var b bool
		if err := n.Decode(&b); err != nil {
			return errAt(n, path, i18n.T("应为 true 或 false"))
		}
		v.SetBool(b)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		i, err := strconv.ParseInt(n.Value, 10, 64)
		if n.Tag != "!!int" || err != nil || v.OverflowInt(i) {
			return errAt(n, path, i18n.T("应为整数"))
		}
		v.SetInt(i)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		i, err := strconv.ParseUint(n.Value, 10, 64)
		if n.Tag != "!!int" || err != nil || v.OverflowUint(i) {
			return errAt(n, path, i18n.T("应为非负整数"))
		}
		v.SetUint(i)
		return nil
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(n.Value, 64)
		if (n.Tag != "!!float" && n.Tag != "!!int") || err != nil || math.IsNaN(f) {
			return errAt(n, path, i18n.T("应为数字"))
		}
		v.SetFloat(f)
		return nil
	}
	return errAt(n, path, fmt.Sprintf("unsupported type %s", v.Type()))
}
