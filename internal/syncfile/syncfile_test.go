package syncfile

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"lanflow/internal/model"
)

func sampleDoc() Doc {
	return Doc{
		Settings: model.Settings{
			CloseToTray: true, Appearance: "dark", Language: "zh", MixedPort: 7890,
			Bypass: []string{"localhost", "*.local"}, TUNStack: "mixed", Mode: model.ModeRule, LogLevel: "info",
			DNS: model.DNSSettings{TakeOver: true, EnhancedMode: "fake-ip", Nameserver: []string{"https://doh.pub/dns-query"},
				Policy: []model.KV{{Key: "geosite:cn", Value: "223.5.5.5"}}},
			Sites:     []model.Site{{Name: "GitHub", URL: "https://github.com"}},
			Shortcuts: []model.Shortcut{{Action: model.ShortcutTUN, Accelerator: "CmdOrCtrl+Shift+T"}},
			SSIDRules: []model.SSIDRule{{SSID: "Office", SubscriptionID: "a1b2c3d4"}},
			WebDAV:    model.WebDAVSettings{URL: "https://dav.example.com/dav", User: "me", Keep: 5},
			Advanced:  model.AdvancedSettings{IPv6: true, LANAllowedIPs: []string{"192.168.0.0/16"}},
			ATrust:    model.ATrustConfig{CorpDomains: []string{"corp.example"}},
		},
		Overrides: []Override{
			{ID: "global", Name: "全局", Enabled: true, Global: true, Text: "log-level: warning\nipv6: false\n"},
			{ID: "e5f6", Name: "远程", Enabled: false, URL: "https://example.com/ov.yaml", Text: "mode: rule\n"},
		},
		CustomRules: []Rule{{Type: "DOMAIN-SUFFIX", Payload: "openai.com", Target: "DIRECT"}},
		Subscriptions: []Subscription{
			{ID: "a1b2c3d4", Name: "机场", URL: SubscriptionRef("a1b2c3d4"), AutoUpdateHours: 12, Overrides: []string{"e5f6"}},
		},
		WebDAVPassword: WebDAVPasswordRef,
	}
}

func TestRoundTrip(t *testing.T) {
	doc := sampleDoc()
	data := Encode(doc)
	got, err := Decode(data, model.Settings{})
	if err != nil {
		t.Fatalf("Decode: %v\n%s", err, data)
	}
	again := Encode(got)
	if !bytes.Equal(data, again) {
		t.Fatalf("not canonical:\n%s\n---\n%s", data, again)
	}
	doc.Format = Format
	if !reflect.DeepEqual(got.Overrides, doc.Overrides) || !reflect.DeepEqual(got.Subscriptions, doc.Subscriptions) ||
		!reflect.DeepEqual(got.CustomRules, doc.CustomRules) || got.WebDAVPassword != doc.WebDAVPassword {
		t.Fatalf("lists differ:\n%+v\n%+v", got, doc)
	}
	if got.Settings.DNS.Policy[0].Value != "223.5.5.5" || got.Settings.Advanced.LANAllowedIPs[0] != "192.168.0.0/16" ||
		got.Settings.SSIDRules[0].SubscriptionID != "a1b2c3d4" || got.Settings.WebDAV.Keep != 5 {
		t.Fatalf("settings lost: %+v", got.Settings)
	}
	// multi-line override text is a literal block, readable by people
	if !strings.Contains(string(data), "text: |") {
		t.Fatalf("override text not a literal block:\n%s", data)
	}
}

func TestMachineLocalItemsStayOut(t *testing.T) {
	doc := sampleDoc()
	doc.Settings.CorePath = "/opt/mihomo"
	doc.Settings.TUN = true
	doc.Settings.UI.ProxySort = "delay"
	doc.Settings.AgentGrants = []model.AgentGrant{{Agent: "evil", Allow: true}}
	doc.Settings.Advanced.InterfaceName = "en0"
	doc.Settings.Sync = model.ConfigSyncSettings{Mode: model.SyncFolder, Dir: "/x"}
	data := string(Encode(doc))
	for _, bad := range []string{"/opt/mihomo", "proxySort", "evil", "en0", "corePath", "agentGrants", "\n  tun:", "sync:"} {
		if strings.Contains(data, bad) {
			t.Errorf("machine-local %q written:\n%s", bad, data)
		}
	}
	// A file that carries them anyway (an older or hand-written one) is
	// read; the items are ignored, and KeepLocal puts this machine's back.
	in := strings.Replace(data, "settings:\n", "settings:\n  corePath: /evil\n  tun: true\n  agentGrants: [{Agent: x, Allow: true}]\n", 1)
	got, warn, err := DecodeWarn([]byte(in), model.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if len(warn) != 0 || got.Settings.CorePath != "" || got.Settings.TUN || got.Settings.AgentGrants != nil {
		t.Fatalf("machine-local items read: %+v %v", got.Settings, warn)
	}
	local := model.Settings{CorePath: "/usr/local/bin/mihomo", TUN: true, OpenAtLogin: true}
	local.UI.ProxyGrid = true
	local.Advanced.TUNDevice = "utun9"
	m := KeepLocal(got.Settings, local)
	if m.CorePath != local.CorePath || !m.TUN || !m.OpenAtLogin || !m.UI.ProxyGrid || m.Advanced.TUNDevice != "utun9" || !m.Advanced.IPv6 {
		t.Fatalf("KeepLocal: %+v", m)
	}
}

func TestDefaultsForMissingKeys(t *testing.T) {
	def := model.Settings{MixedPort: 7890, CloseToTray: true, Mode: model.ModeRule}
	got, err := Decode([]byte("format: 1\nsettings:\n  mode: global\n"), def)
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings.MixedPort != 7890 || !got.Settings.CloseToTray || got.Settings.Mode != model.ModeGlobal {
		t.Fatalf("defaults not kept: %+v", got.Settings)
	}
}

func TestDecodeErrorsHaveLines(t *testing.T) {
	cases := []struct {
		name, text string
		line       int
		has        string
	}{
		{"syntax", "format: 1\nsettings:\n  mode: [\n", 3, "YAML"},
		{"type", "format: 1\nsettings:\n  mixedPort: abc\n", 3, "settings.mixedPort"},
		{"bool", "format: 1\nsettings:\n  allowLAN: yes please\n", 3, "true"},
		{"no format", "settings: {}\n", 1, "format"},
		{"newer", "format: 9\n", 1, "9"},
		{"list", "format: 1\ncustomRules: {}\n", 2, "customRules"},
		{"rule", "format: 1\ncustomRules:\n  - type: DOMAIN\n    payload: a.com\n", 3, "customRules[0]"},
		{"sub url", "format: 1\nsubscriptions:\n  - id: ab\n    name: x\n    url: ftp://x\n", 5, "subscriptions[0].url"},
		{"dup sub", "format: 1\nsubscriptions:\n  - {id: ab, name: x, url: ''}\n  - {id: ab, name: y, url: ''}\n", 4, "重复"},
		{"dangling override", "format: 1\nsubscriptions:\n  - id: ab\n    name: x\n    url: ''\n    overrides: [nope]\n", 6, "nope"},
		{"bad override yaml", "format: 1\noverrides:\n  - id: g\n    name: g\n    enabled: true\n    global: true\n    text: \"a: [\"\n", 7, "YAML"},
		{"dup key", "format: 1\nsettings:\n  mode: rule\n  mode: global\n", 4, "重复"},
	}
	for _, c := range cases {
		_, err := Decode([]byte(c.text), model.Settings{})
		e, ok := err.(*Error)
		if !ok {
			t.Errorf("%s: want *Error, got %v", c.name, err)
			continue
		}
		if e.Line != c.line || !strings.Contains(e.Error(), c.has) {
			t.Errorf("%s: got line %d %q, want line %d containing %q", c.name, e.Line, e.Error(), c.line, c.has)
		}
	}
}

func TestUnknownKeysWarn(t *testing.T) {
	_, warn, err := DecodeWarn([]byte("format: 1\nsettings:\n  futureThing: 3\n"), model.Settings{})
	if err != nil || len(warn) != 1 || warn[0].Line != 3 || warn[0].Path != "settings.futureThing" {
		t.Fatalf("got %v %v", warn, err)
	}
}

func TestSniff(t *testing.T) {
	if !Sniff(Encode(sampleDoc())) {
		t.Fatal("lanflow.yaml not recognised")
	}
	if Sniff([]byte(`{"app":"lanflow","format":1}`)) || Sniff([]byte("proxies: []\n")) {
		t.Fatal("JSON export or a profile taken for lanflow.yaml")
	}
}

func TestLowerCamel(t *testing.T) {
	for in, want := range map[string]string{
		"TUNStack": "tunStack", "DNS": "dns", "ATrustCompat": "aTrustCompat", "HTTPPorts": "httpPorts",
		"SSIDRules": "ssidRules", "MixedPort": "mixedPort", "IPv6": "ipv6", "URL": "url", "AllowLAN": "allowLAN",
	} {
		if got := lowerCamel(in); got != want {
			t.Errorf("lowerCamel(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestConflictCopies(t *testing.T) {
	for name, want := range map[string]bool{
		"lanflow 2.yaml": true, "lanflow (1).yaml": true, "lanflow (Ann's conflicted copy 2026-01-02).yaml": true,
		"lanflow-DESKTOP-1AB2C3D.yaml": true, "lanflow.sync-conflict-20260102-150405-ABC.yaml": true,
		".lanflow 2.yaml.icloud": true,
		"lanflow.yaml":           false, ".lanflow.yaml.icloud": false, ".lanflow.yaml.tmp-123": false, "lanflow.yml": false,
		"notes.yaml": false, "lanflow 2.json": false,
	} {
		if got := IsConflictCopy(name); got != want {
			t.Errorf("IsConflictCopy(%q) = %v, want %v", name, got, want)
		}
	}
	dir := t.TempDir()
	for _, n := range []string{"lanflow 2.yaml", "lanflow (1).yaml", "other.txt", PlaceholderName(FileName)} {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600)
	}
	s := Look(dir)
	if s.Exists || !s.Placeholder || len(s.Conflicts) != 2 || s.Conflicts[0] != "lanflow (1).yaml" {
		t.Fatalf("Look: %+v", s)
	}
	os.WriteFile(filepath.Join(dir, FileName), []byte("format: 1\n"), 0o600)
	s2 := Look(dir)
	if !s2.Exists || s2.Placeholder || s2.Same(s) {
		t.Fatalf("Look after download: %+v", s2)
	}
	time.Sleep(10 * time.Millisecond)
	if !Look(dir).Same(s2) {
		t.Fatal("unchanged folder looks different")
	}
}

// TestSchemaUpToDate keeps docs/schema/lanflow.schema.json equal to what
// Doc generates. Regenerate with:
//
//	UPDATE_SCHEMA=1 go test ./internal/syncfile -run TestSchemaUpToDate
func TestSchemaUpToDate(t *testing.T) {
	path := filepath.Join("..", "..", filepath.FromSlash(SchemaPath))
	want := Schema()
	if os.Getenv("UPDATE_SCHEMA") != "" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with UPDATE_SCHEMA=1 to create it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is out of date with syncfile.Doc; run UPDATE_SCHEMA=1 go test ./internal/syncfile -run TestSchemaUpToDate", SchemaPath)
	}
}

// TestSchemaMatchesEncoding checks every key Encode writes is a schema
// property and every schema property is a Go field Encode writes: the
// schema and the Go structs describe the same file.
func TestSchemaMatchesEncoding(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false {
		t.Fatal("root must refuse unknown keys")
	}
	inSchema := map[string]bool{}
	var walkSchema func(s map[string]any, path string)
	walkSchema = func(s map[string]any, path string) {
		if props, ok := s["properties"].(map[string]any); ok {
			for k, v := range props {
				p := join(path, k)
				inSchema[p] = true
				walkSchema(v.(map[string]any), p)
			}
		}
		if items, ok := s["items"].(map[string]any); ok {
			walkSchema(items, path+"[]")
		}
	}
	walkSchema(schema, "")

	// every key of a fully populated document
	doc := sampleDoc()
	var root yaml.Node
	if err := yaml.Unmarshal(Encode(doc), &root); err != nil {
		t.Fatal(err)
	}
	written := map[string]bool{}
	var walkYAML func(n *yaml.Node, path string)
	walkYAML = func(n *yaml.Node, path string) {
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				p := join(path, n.Content[i].Value)
				written[p] = true
				walkYAML(n.Content[i+1], p)
			}
		case yaml.SequenceNode:
			for _, c := range n.Content {
				walkYAML(c, path+"[]")
			}
		}
	}
	walkYAML(root.Content[0], "")
	for p := range written {
		if !inSchema[p] {
			t.Errorf("written key %s is not in the schema", p)
		}
	}
	var missing []string
	for _, p := range SchemaPaths() {
		if !inSchema[p] {
			t.Errorf("Go field %s is not in the schema", p)
		}
	}
	for p := range inSchema {
		if !contains(SchemaPaths(), p) {
			missing = append(missing, p)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("schema properties without a Go field: %v", missing)
	}
	// The settings section mirrors model.Settings minus Excluded.
	for _, e := range Excluded {
		if inSchema[e.Path] {
			t.Errorf("excluded %s is in the schema", e.Path)
		}
		if !strings.HasPrefix(e.Path, "settings.") {
			t.Errorf("excluded %s is not a setting", e.Path)
		}
	}
	if pathKey("customRules[3].type") != "customRules[].type" {
		t.Fatal("pathKey")
	}
}

// TestExcludedNamesExist guards against a renamed field silently leaving
// the exclusion list stale (the item would then sync).
func TestExcludedNamesExist(t *testing.T) {
	all := map[string]bool{}
	var walk func(rt reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		for i := 0; i < rt.NumField(); i++ {
			name, _, skip := keyOf(rt.Field(i))
			if skip {
				continue
			}
			p := join(path, name)
			all[p] = true
			if ft := rt.Field(i).Type; ft.Kind() == reflect.Struct && ft != timeType {
				walk(ft, p)
			}
		}
	}
	walk(reflect.TypeOf(model.Settings{}), "settings")
	for _, e := range Excluded {
		if !all[e.Path] {
			t.Errorf("excluded %s names no field of model.Settings", e.Path)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
