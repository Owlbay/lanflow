package app

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"lanflow/internal/applog"
	"lanflow/internal/i18n"
)

// 导出诊断信息: one zip with what is needed to look into a problem —
// Lanflow's and the core's logs, the helper's logs, the runtime config,
// the settings, the state of the app and of the system network (routes,
// DNS resolvers, system proxy). Nothing secret leaves: proxy servers and
// credentials are removed from configs, URLs keep only their host, and
// token / password style values are masked everywhere (applog.Redact).

const diagTailBytes = 2 << 20 // at most the last 2 MB of each log

// ExportDiagnostics implements API.ExportDiagnostics.
func (s *Service) ExportDiagnostics(path string) (err error) {
	defer func() { s.logOp("export diagnostics "+filepath.Base(path), err) }()
	if strings.TrimSpace(path) == "" {
		return errors.New(i18n.T("没有选择保存位置。"))
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	now := s.d.now()
	for _, f := range s.diagFiles(false, true) {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: now})
		if err == nil {
			_, _ = w.Write(f.data)
		}
	}

	if err := zw.Close(); err != nil {
		return fmt.Errorf(i18n.T("生成诊断信息失败：%v"), err)
	}
	if err := writeFileAtomic(path, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf(i18n.T("写入 %s 失败：%v"), path, err)
	}
	return nil
}

// diagFile is one file of the diagnostics bundle.
type diagFile struct {
	name string
	data []byte
}

// diagFiles collects the diagnostics bundle. For a feedback report
// (feedback true) it is stricter: core logs keep only warnings and
// errors without connection lines, domain lists (rules, hosts, bypass,
// DNS policies, Wi-Fi names…) are left out of the configs, public IP
// addresses are masked, logs are cut shorter, and Lanflow's own log
// only comes in when withLog.
func (s *Service) diagFiles(feedback, withLog bool) []diagFile {
	var out []diagFile
	add := func(name string, data []byte) {
		if feedback {
			data = []byte(redactReport(string(data)))
		}
		out = append(out, diagFile{name, data})
	}
	now := s.d.now()
	tail := int64(diagTailBytes)
	readme := diagReadme
	if feedback {
		tail = feedbackTailBytes
		readme = feedbackReadme
	}
	add("README.txt", []byte(readme))
	add("system.txt", []byte(s.diagSystem(now, withLog)))

	s.mu.Lock()
	set := copySettings(s.state.settings)
	runtimeYAML := append([]byte(nil), s.state.runtimeYAML...)
	ov := s.state.overrides
	s.mu.Unlock()
	if data, err := json.MarshalIndent(set, "", "  "); err == nil {
		if feedback {
			data = dropPrivateJSON(data)
		}
		add("config/settings.json", []byte(applog.Redact(string(data))))
	}
	conf := redactConfig
	if feedback {
		conf = func(b []byte) []byte { return dropPrivateYAML(redactConfig(b)) }
	}
	if len(runtimeYAML) == 0 {
		runtimeYAML, _ = os.ReadFile(s.runtime)
	}
	if len(runtimeYAML) > 0 {
		add("config/runtime.yaml", conf(runtimeYAML))
	}
	if strings.TrimSpace(ov.Global) != "" {
		add("config/override-global.yaml", conf([]byte(ov.Global)))
	}
	ids := make([]string, 0, len(ov.PerSub))
	for id := range ov.PerSub {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if text := ov.PerSub[id]; strings.TrimSpace(text) != "" {
			add("config/override-"+safeName(id)+".yaml", conf([]byte(text)))
		}
	}

	logs := []struct {
		name, path string
		core       bool
	}{
		{"logs/lanflow.log", s.alog.Path(), false},
		{"logs/lanflow.log.1", s.alog.Path() + ".1", false},
		{"logs/mihomo.log", s.logFile, true},
		{"logs/mihomo.log.1", s.logFile + ".1", true},
		{"logs/mihomo-tun.log", filepath.Join(s.workDir, "mihomo-tun.log"), true},
		{"logs/mihomo-tun.log.1", filepath.Join(s.workDir, "mihomo-tun.log.1"), true},
		{"logs/helper.log", filepath.Join(helperLogDir, "helper.log"), false},
		{"logs/helper-install.log", filepath.Join(helperLogDir, "install.log"), false},
	}
	for _, l := range logs {
		if l.path == "" || l.path == ".1" {
			continue
		}
		if feedback && !withLog && strings.HasPrefix(l.name, "logs/lanflow.log") {
			continue
		}
		data, err := readTail(l.path, tail)
		if err != nil || len(data) == 0 {
			continue
		}
		if feedback && l.core {
			data = coreProblems(data)
			if len(data) == 0 {
				continue
			}
		}
		if l.core {
			data = dropCaptureLines(data) // capture.go
		}
		add(l.name, []byte(applog.Redact(string(data))))
	}

	ctx, cancel := s.ctrlCtx(nil, 20*time.Second)
	defer cancel()
	cmds := s.d.diagCommands
	if cmds == nil {
		cmds = systemDiagnostics
	}
	outs := cmds(ctx)
	names := make([]string, 0, len(outs))
	for name := range outs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		add("network/"+name, []byte(applog.Redact(outs[name])))
	}
	return out
}

const diagReadme = `Lanflow diagnostics

system.txt    app and core state, network info, subscriptions (host only)
config/       Lanflow settings, the runtime config and overrides
logs/         Lanflow, core (mihomo), TUN core and helper logs (last 2 MB each)
network/      routes, DNS resolvers and the system proxy as the OS reports them

Proxy servers, passwords, UUIDs and keys are removed; links keep only their
host; token / password values are masked. Domains you visited still appear
in the core log: look through the files before sharing them.
`

// diagSystem is system.txt.
func (s *Service) diagSystem(now time.Time, withLog bool) string {
	var b strings.Builder
	line := func(k string, v any) { fmt.Fprintf(&b, "%-22s %v\n", k+":", v) }
	line("time", now.Format(time.RFC3339))
	line("os", runtime.GOOS+"/"+runtime.GOARCH)
	line("os version", osVersion())
	st := s.Status()
	line("core", st.Core)
	line("core version", st.CoreVersion)
	if st.CoreError != "" {
		line("core error", st.CoreError)
	}
	line("started at", st.StartedAt.Format(time.RFC3339))
	line("mode", st.Mode)
	line("mixed port", st.MixedPort)
	line("allow LAN", st.AllowLAN)
	line("system proxy (ours)", st.SystemProxy)
	line("TUN", st.TUN)
	line("helper installed", st.TUNAvailable)
	line("connections", st.Connections)
	line("memory", st.Memory)
	s.mu.Lock()
	bin := s.state.coreBin
	s.mu.Unlock()
	line("core binary", bin)
	if a := st.ATrust; a.Running {
		line("aTrust", fmt.Sprintf("processes %v, interfaces %v, ranges %v, domains %v, DNS %v", a.Processes, a.Interfaces, a.CIDRs, a.Domains, a.DNS))
	} else {
		line("aTrust", "not detected")
	}
	n := s.NetInfo()
	line("interface", n.Interface)
	line("LAN IP", n.LANIP)
	line("gateway", n.Gateway)
	line("system DNS", strings.Join(n.DNS, ", "))
	line("public IP lookup", strings.TrimSpace(n.PublicIPStatus+" "+n.PublicIPError))
	if cur, err := s.d.sys.Get(); err == nil {
		line("system proxy now", fmt.Sprintf("enabled=%v %s:%d", cur.Enabled, cur.Host, cur.Port))
	}
	eff := s.EffectiveDNS()
	line("DNS source", eff.Source)
	line("DNS mode", eff.Settings.EnhancedMode)

	b.WriteString("\nsubscriptions:\n")
	for _, sub := range s.Subscriptions() {
		cur := " "
		if sub.ID == st.CurrentSubscription {
			cur = "*"
		}
		src := sub.Source
		if sub.URL != "" {
			src += " " + applog.Redact(sub.URL)
		}
		fmt.Fprintf(&b, " %s %s [%s] updated %s auto %dh", cur, sub.Name, src, sub.UpdatedAt.Format(time.RFC3339), sub.AutoUpdateHours)
		if sub.LastError != "" {
			fmt.Fprintf(&b, " error: %s", applog.Redact(sub.LastError))
		}
		b.WriteString("\n")
	}
	if !withLog {
		return b.String()
	}
	b.WriteString("\nrecent Lanflow log:\n")
	es := s.alog.Entries()
	if len(es) > 50 {
		es = es[len(es)-50:]
	}
	for _, e := range es {
		fmt.Fprintf(&b, " %s %-7s %s\n", e.Time.Format("15:04:05"), e.Level, e.Payload)
	}
	return b.String()
}

// readTail returns at most the last n bytes of a file, starting at a line.
func readTail(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() <= n {
		return io.ReadAll(f)
	}
	if _, err := f.Seek(st.Size()-n, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(f)
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		data = data[i+1:]
	}
	return data, err
}

// sensitiveKeys are config keys whose values never leave the machine.
var sensitiveKeys = map[string]bool{
	"password": true, "uuid": true, "private-key": true, "pre-shared-key": true, "psk": true,
	"auth": true, "auth-str": true, "auth_str": true, "token": true, "secret": true,
	"obfs-password": true, "public-key": true, "short-id": true, "username": true,
	"user": true, "passwd": true, "id": true, "key": true, "certificate": true,
	"server": true, "servername": true, "sni": true, "host": true, "ip": true, "ipv6": false,
	"peers": true, "reserved": true, "headers": true, "ws-opts": true, "grpc-opts": true,
	"h2-opts": true, "http-opts": true, "reality-opts": true, "ech-opts": true, "plugin-opts": true,
}

// redactConfig removes servers and credentials from a Clash/Mihomo YAML
// document; URLs keep only their host. Text that does not parse is
// masked line by line instead.
func redactConfig(data []byte) []byte {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		return []byte(applog.Redact(string(data)))
	}
	redactCaptureLayer(&doc) // capture.go: only whether it is on and how many items
	var walk func(n *yaml.Node, inProxy bool)
	walk = func(n *yaml.Node, inProxy bool) {
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, c := range n.Content {
				walk(c, inProxy)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				key := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(k.Value, "+"), "!"), "+")
				if inProxy && sensitiveKeys[strings.ToLower(key)] {
					*v = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "***"}
					continue
				}
				if !inProxy && (key == "secret" || key == "password" || key == "authentication") {
					*v = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "***"}
					continue
				}
				walk(v, inProxy || key == "proxies" || key == "dialer-proxy-config")
			}
		case yaml.ScalarNode:
			n.Value = applog.Redact(n.Value)
		case yaml.AliasNode:
		}
	}
	walk(&doc, false)
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return []byte(applog.Redact(string(data)))
	}
	return out
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == '.' {
			return '_'
		}
		return r
	}, s)
}
