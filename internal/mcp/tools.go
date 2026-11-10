package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"lanflow/internal/app"
	"lanflow/internal/app/remote"
	"lanflow/internal/rpc"
)

// Tool is one MCP tool as listed.
type Tool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

type tool struct {
	Tool
	method    rpc.Method // generated tools
	composite func(ctx context.Context, c *remote.Client, a map[string]json.RawMessage) (any, error)
}

// offered reports whether an MCP client gets a tool for m: reads,
// sensitive reads (asked per connection) and writes (asked once per
// agent). Dangerous and window-only methods are never offered, nor what
// agents cannot use (whole-settings writes, image bytes).
func offered(m rpc.Method) bool {
	switch m.Level {
	case rpc.LevelRead, rpc.LevelSensitive, rpc.LevelWrite:
	default:
		return false
	}
	switch m.Name {
	case "OnChange", "SaveSettings", "ApplyDNS", "ApplySniffer", "IconData", "ProcessIcon", "Methods",
		"ConfigDir", "LogDir", "ExportATrustConfig", "ImportATrustConfig", "ExportDiagnostics":
		return false
	}
	return true
}

// ToolName turns a method name into snake_case: SelectProxy →
// select_proxy, DNSQuery → dns_query, ImportSubscriptionURL →
// import_subscription_url.
func ToolName(method string) string {
	if strings.HasPrefix(method, "ATrust") {
		method = "Atrust" + strings.TrimPrefix(method, "ATrust")
	}
	r := []rune(method)
	var b strings.Builder
	for i, c := range r {
		if unicode.IsUpper(c) {
			prevLower := i > 0 && (unicode.IsLower(r[i-1]) || unicode.IsDigit(r[i-1]))
			nextLower := i+1 < len(r) && unicode.IsLower(r[i+1])
			if i > 0 && (prevLower || (nextLower && unicode.IsUpper(r[i-1]))) {
				b.WriteByte('_')
			}
			b.WriteRune(unicode.ToLower(c))
			continue
		}
		b.WriteRune(c)
	}
	return b.String()
}

var levelNote = map[rpc.Level]string{
	rpc.LevelRead:      "",
	rpc.LevelSensitive: " [sensitive: the user must approve in Lanflow, once per session]",
	rpc.LevelWrite:     " [write: the first write asks the user in Lanflow; audited; revert with undo]",
}

var apiType = reflect.TypeOf((*app.API)(nil)).Elem()

// agentParamTypes are the Go types of the agent methods' parameters.
var agentParamTypes = map[string]reflect.Type{
	"map[string]any": reflect.TypeOf(map[string]any{}),
	"bool":           reflect.TypeOf(false),
	"int":            reflect.TypeOf(0),
}

func generatedTool(m rpc.Method) tool {
	props := map[string]any{}
	var types []reflect.Type
	if am, ok := apiType.MethodByName(m.Name); ok {
		for i := 0; i < am.Type.NumIn(); i++ {
			types = append(types, am.Type.In(i))
		}
	} else {
		for _, p := range m.Params {
			types = append(types, agentParamTypes[p.Type])
		}
	}
	var required []string
	for i, p := range m.Params {
		var sc map[string]any
		if i < len(types) && types[i] != nil {
			sc = schemaOf(types[i], 0)
		} else {
			sc = map[string]any{}
		}
		props[p.Name] = sc
		if !(m.Name == "PatchSettings" && p.Name == "dryRun") {
			required = append(required, p.Name)
		}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	doc := m.Doc
	if doc == "" {
		doc = m.Name
	}
	ann := map[string]any{"readOnlyHint": m.Level == rpc.LevelRead || m.Level == rpc.LevelSensitive}
	if m.Level == rpc.LevelWrite {
		ann["destructiveHint"] = false
	}
	return tool{Tool: Tool{Name: ToolName(m.Name), Description: doc + levelNote[m.Level], InputSchema: schema, Annotations: ann}, method: m}
}

var timeType = reflect.TypeOf(time.Time{})

// schemaOf is a JSON Schema for values of t as encoding/json writes them.
func schemaOf(t reflect.Type, depth int) map[string]any {
	if depth > 6 {
		return map[string]any{}
	}
	switch t {
	case timeType:
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return schemaOf(t.Elem(), depth)
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Uint8:
		return map[string]any{"type": "integer", "minimum": 0, "maximum": 255}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "contentEncoding": "base64"}
		}
		return map[string]any{"type": "array", "items": schemaOf(t.Elem(), depth+1)}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": schemaOf(t.Elem(), depth+1)}
	case reflect.Struct:
		props := map[string]any{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := f.Name
			if tag := f.Tag.Get("json"); tag != "" {
				n, _, _ := strings.Cut(tag, ",")
				if n == "-" {
					continue
				}
				if n != "" {
					name = n
				}
			}
			props[name] = schemaOf(f.Type, depth+1)
		}
		return map[string]any{"type": "object", "properties": props}
	case reflect.Interface:
		return map[string]any{}
	}
	return map[string]any{}
}

var (
	toolsOnce sync.Once
	toolList  []tool
	toolIndex map[string]int
)

func buildTools() {
	for _, m := range rpc.Manifest() {
		if offered(m) {
			toolList = append(toolList, generatedTool(m))
		}
	}
	toolList = append(toolList, compositeTools()...)
	sort.SliceStable(toolList, func(i, j int) bool { return toolList[i].Name < toolList[j].Name })
	toolIndex = map[string]int{}
	for i, t := range toolList {
		toolIndex[t.Name] = i
	}
}

// Tools lists the tools offered.
func Tools() []Tool {
	toolsOnce.Do(buildTools)
	out := make([]Tool, len(toolList))
	for i, t := range toolList {
		out[i] = t.Tool
	}
	return out
}

func toolByName(name string) (tool, bool) {
	toolsOnce.Do(buildTools)
	i, ok := toolIndex[name]
	if !ok {
		return tool{}, false
	}
	return toolList[i], true
}
