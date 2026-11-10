package syncfile

import (
	"encoding/json"
	"reflect"
	"strings"
)

// SchemaPath is where the generated schema lives in the repository
// (relative to its root); TestSchemaUpToDate keeps it in step with Doc.
const SchemaPath = "docs/schema/lanflow.schema.json"

// enums are the values the app accepts for some strings; anything else
// is replaced by the default when loaded (app.normalizeSettings), so the
// schema says so up front.
var enums = map[string][]string{
	"settings.appearance":                 {"system", "light", "dark"},
	"settings.language":                   {"zh", "en"},
	"settings.tunStack":                   {"mixed", "system", "gvisor"},
	"settings.mode":                       {"rule", "global", "direct"},
	"settings.logLevel":                   {"debug", "info", "warning", "error", "silent"},
	"settings.coreChannel":                {"stable", "alpha"},
	"settings.dns.enhancedMode":           {"fake-ip", "redir-host"},
	"settings.geo.mode":                   {"db", "dat"},
	"settings.webDAV.autoBackup":          {"", "daily", "weekly"},
	"settings.advanced.findProcessMode":   {"", "off", "strict", "always"},
	"settings.advanced.geodataLoader":     {"", "memconservative", "standard"},
	"settings.advanced.dnsCacheAlgorithm": {"", "lru", "arc"},
}

// patterns constrain some strings further.
var patterns = map[string]string{
	"subscriptions[].url": `^$|^keychain:.+$|^https?://`,
	"subscriptions[].id":  `^[A-Za-z0-9_-]{1,64}$`,
	"overrides[].id":      `^[A-Za-z0-9_-]{1,64}$`,
	"overrides[].url":     `^https?://`,
	"webdavPassword":      `^keychain:.+$`,
}

// descriptions explain the top-level keys.
var descriptions = map[string]string{
	"":               "Lanflow configuration file lanflow.yaml (docs/config-sync.md)",
	"format":         "File format version; this version writes and reads 1",
	"settings":       "Settings, without the machine-local items (docs/config-sync.md). A missing key means the default value",
	"overrides":      "The override list, in the order the overrides apply",
	"customRules":    "Custom rules, in matching order, before the subscription's rules",
	"subscriptions":  "Subscriptions with a link; the link is a keychain:<id> reference (a plain http(s) link is accepted too)",
	"webdavPassword": "keychain:webdav-password when a WebDAV password is set; the password itself stays in the keychain",
}

// Schema returns the JSON Schema (draft 2020-12) of lanflow.yaml,
// generated from Doc.
func Schema() []byte {
	s := schemaOf(reflect.TypeOf(Doc{}), "")
	s["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	s["title"] = "lanflow.yaml"
	s["required"] = []string{"format"}
	props := s["properties"].(map[string]any)
	props["format"] = map[string]any{"const": Format, "description": descriptions["format"]}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(data, '\n')
}

func schemaOf(t reflect.Type, path string) map[string]any {
	out := map[string]any{}
	if d, ok := descriptions[path]; ok {
		out["description"] = d
	}
	if t == timeType {
		out["type"] = "string"
		out["description"] = "RFC 3339 time; empty for none"
		return out
	}
	switch t.Kind() {
	case reflect.Pointer:
		return schemaOf(t.Elem(), path)
	case reflect.Struct:
		out["type"] = "object"
		out["additionalProperties"] = false
		props := map[string]any{}
		for _, f := range fieldsOf(t, path) {
			props[f.name] = schemaOf(t.Field(f.index).Type, f.path)
		}
		out["properties"] = props
	case reflect.Slice, reflect.Array:
		out["type"] = []string{"array", "null"}
		out["items"] = schemaOf(t.Elem(), path+"[]")
	case reflect.Map:
		out["type"] = []string{"object", "null"}
		out["additionalProperties"] = schemaOf(t.Elem(), path+"{}")
	case reflect.String:
		out["type"] = []string{"string", "null"}
		if e, ok := enums[path]; ok {
			out["enum"] = e
		}
		if p, ok := patterns[path]; ok {
			out["pattern"] = p
		}
	case reflect.Bool:
		out["type"] = "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		out["type"] = "integer"
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		out["type"] = "integer"
		out["minimum"] = 0
	case reflect.Float32, reflect.Float64:
		out["type"] = "number"
	default:
		panic("syncfile: no schema for " + t.String() + " at " + path)
	}
	return out
}

// SchemaPaths lists every property path the schema describes (for tests
// and docs), "[]" marking list items.
func SchemaPaths() []string {
	var out []string
	var walk func(t reflect.Type, path string)
	walk = func(t reflect.Type, path string) {
		switch {
		case t == timeType:
		case t.Kind() == reflect.Pointer:
			walk(t.Elem(), path)
		case t.Kind() == reflect.Struct:
			for _, f := range fieldsOf(t, path) {
				out = append(out, f.path)
				walk(t.Field(f.index).Type, f.path)
			}
		case t.Kind() == reflect.Slice:
			walk(t.Elem(), path+"[]")
		case t.Kind() == reflect.Map:
			walk(t.Elem(), path+"{}")
		}
	}
	walk(reflect.TypeOf(Doc{}), "")
	return out
}

// pathKey normalises a concrete path ("customRules[3].type") to its
// schema form ("customRules[].type").
func pathKey(p string) string {
	var b strings.Builder
	skip := false
	for _, r := range p {
		switch {
		case r == '[':
			b.WriteString("[]")
			skip = true
		case r == ']':
			skip = false
		case !skip:
			b.WriteRune(r)
		}
	}
	return b.String()
}
