// Package fake is an in-memory app.API with realistic data and simulated
// traffic, for UI development (LANFLOW_FAKE=1) and UI tests.
//
// It may grow sample data and behaviour, but must keep implementing
// app.API.
package fake

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/geo"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/seqring"
)

// Fake implements app.API.
type Fake struct {
	mu             sync.Mutex
	appLogsCleared bool
	st             model.Status
	up, down       []int64
	px             model.ProxySnapshot
	subs           []model.Subscription
	conns          []model.Connection
	closed         []model.Connection
	logs           []model.LogEntry
	// sequence numbers for the incremental reads (pages.go)
	logTotal, logEpoch uint64
	closedTotal        uint64
	connVer            *seqring.Versions[model.Connection]
	rules              []model.Rule
	custom             []model.CustomRule
	provs              []model.RuleProvider
	pprovs             []model.ProxyProvider
	set                model.Settings
	ovl                []model.Override // the override list; Overrides() derives the legacy views
	sites              []model.SiteCheck
	notices            []app.Notice
	helper             bool
	subsFns            map[int]func(app.Topic)
	nextFn             int
	stop               chan struct{}
	Delay              time.Duration // simulated action latency (default 600 ms; 0 in tests via NewInstant)
	coreDL             model.CoreDownload
	leak               model.LeakResult      // 泄漏检查 result (RunLeakCheck)
	probe              model.ProbeState      // 检测 page state (probe.go)
	statsReset         bool                  // ResetTrafficStats was called: TrafficStats is empty
	f2                 *feat2                // WebDAV backup and mirror state (feat2.go), allocated on first use
	cores              []model.InstalledCore // installed Mihomo versions (CoreReleases / InstalledCores / UseCore…)
	cachesCleaned      bool                  // CleanCaches was called: CacheUsage reports the core cache only
	syncTest           *SyncTest             // configuration sync answers (configsync.go)
}

var _ app.API = (*Fake)(nil)

var nodes = []string{"香港 01", "香港 02", "东京 03", "东京 04", "新加坡 02", "台湾 01", "美国 05", "美国 06", "英国 01"}

// New returns a running fake with a ticking simulation.
func New() *Fake { f := build(); f.Delay = 600 * time.Millisecond; go f.loop(); return f }

// NewInstant returns a fake without background ticking and without delays (for tests).
func NewInstant() *Fake { return build() }

func build() *Fake {
	f := &Fake{subsFns: map[int]func(app.Topic){}, stop: make(chan struct{})}
	now := time.Now()
	f.st = model.Status{Core: model.CoreRunning, CoreVersion: "v1.19.33", StartedAt: now.Add(-2*time.Hour - 13*time.Minute),
		SystemProxy: true, Mode: model.ModeRule, MixedPort: 7890, LANAddress: "192.168.1.23",
		TotalUp: 21 << 20, TotalDown: 1 << 30, Memory: 48 << 20, CurrentSubscription: "s1", HasSubscriptions: true}
	for i := 0; i < 60; i++ {
		f.up = append(f.up, int64(10e3+rand.Intn(30e3)))
		f.down = append(f.down, int64(300e3+rand.Intn(1500e3)))
	}
	pm := map[string]model.Proxy{"DIRECT": {Name: "DIRECT", Type: "Direct"}, "REJECT": {Name: "REJECT", Type: "Reject"}}
	delays := []int{142, 238, 86, 312, 412, 188, -1, 920, 0}
	types := []string{"Shadowsocks", "Vmess", "Trojan", "Hysteria2", "Vless", "Shadowsocks", "Trojan", "Vmess", "Shadowsocks"}
	for i, n := range nodes {
		pm[n] = model.Proxy{Name: n, Type: types[i], UDP: true, Delay: delays[i]}
	}
	groups := []model.ProxyGroup{
		{Name: "节点选择", Type: model.GroupSelect, Now: "东京 03", All: append([]string{"自动选择"}, nodes...)},
		{Name: "自动选择", Type: model.GroupURLTest, Now: "香港 01", All: nodes[:6]},
		{Name: "故障转移", Type: model.GroupFallback, Now: "东京 03", All: []string{"东京 03", "香港 01", "新加坡 02"}},
		{Name: "流媒体", Type: model.GroupSelect, Now: "香港 01", All: []string{"节点选择", "香港 01", "东京 03", "新加坡 02", "台湾 01"}},
		{Name: "GLOBAL", Type: model.GroupSelect, Now: "节点选择", All: append([]string{"DIRECT", "REJECT", "节点选择", "自动选择", "故障转移", "流媒体"}, nodes...)},
	}
	for _, g := range groups {
		pm[g.Name] = model.Proxy{Name: g.Name, Type: string(g.Type)}
	}
	f.px = model.ProxySnapshot{Groups: groups, Proxies: pm, MainGroup: "节点选择"}
	f.pprovs = []model.ProxyProvider{
		{Name: "机场节点", VehicleType: "HTTP", Proxies: append([]string(nil), nodes[:6]...), TestURL: "https://www.gstatic.com/generate_204",
			UpdatedAt: now.Add(-3 * time.Hour), Upload: 512 << 20, Download: 32 << 30, Total: 200 << 30, Expire: time.Date(2027, 3, 1, 0, 0, 0, 0, time.Local)},
		{Name: "自建节点", VehicleType: "File", Proxies: append([]string(nil), nodes[6:]...), TestURL: "https://www.gstatic.com/generate_204", UpdatedAt: now.Add(-2 * 24 * time.Hour)},
	}
	for _, pp := range f.pprovs {
		for _, n := range pp.Proxies {
			p := pm[n]
			p.Provider = pp.Name
			pm[n] = p
		}
	}
	f.subs = []model.Subscription{
		{ID: "s1", Name: "我的机场", Source: "url", URL: "https://sub.example.com/api/v1/client/subscribe?token=a1b2c3", Current: true, Download: 32 << 30, Upload: 512 << 20, Total: 200 << 30, Expire: time.Date(2027, 3, 1, 0, 0, 0, 0, time.Local), UpdatedAt: now.Add(-10 * time.Minute), AutoUpdateHours: 12},
		{ID: "s2", Name: "备用机场", Source: "url", URL: "https://backup.example.net/link/xyz?clash=1", Download: 180 << 30, Total: 200 << 30, Expire: now.Add(4 * 24 * time.Hour), UpdatedAt: now.Add(-72 * time.Hour), LastError: "更新失败：连接超时。仍在使用上次的配置。", AutoUpdateHours: 24},
		{ID: "s3", Name: "本地测试", Source: "file", FilePath: "/Users/me/config.yaml", UpdatedAt: now.Add(-26 * time.Hour)},
	}
	src := [][6]string{
		{"www.google.com:443", "Safari", "DomainSuffix", "google.com", "节点选择", "142.250.196.100"},
		{"api.github.com:443", "git", "GeoSite", "github", "节点选择", "20.205.243.168"},
		{"192.168.1.1:80", "", "IPCIDR", "192.168.0.0/16", "DIRECT", "192.168.1.1"},
		{"i.ytimg.com:443", "Google Chrome", "GeoSite", "youtube", "流媒体", "142.250.66.54"},
		{"rr3---sn-i3b7kn7s.googlevideo.com:443", "Google Chrome", "GeoSite", "youtube", "流媒体", "173.194.55.8"},
		{"slack.com:443", "Slack", "DomainSuffix", "slack.com", "节点选择", "52.68.12.4"},
		{"www.apple.com:443", "Safari", "GeoIP", "CN", "DIRECT", "23.40.12.86"},
		{"api.openai.com:443", "Python", "DomainSuffix", "openai.com", "节点选择", "104.18.33.45"},
		{"www.bilibili.com:443", "Safari", "GeoSite", "cn", "DIRECT", "120.92.78.97"},
		{"telegram.org:443", "Telegram", "IPCIDR", "149.154.160.0/20", "节点选择", "149.154.167.99"},
		{"open.spotify.com:443", "Spotify", "DomainSuffix", "spotify.com", "流媒体", "35.186.224.25"},
		{"www.notion.so:443", "Notion", "DomainSuffix", "notion.so", "节点选择", "104.18.22.110"},
		{"discord.com:443", "Discord", "DomainSuffix", "discord.com", "节点选择", "162.159.128.233"},
		{"time.apple.com:123", "timed", "GeoIP", "CN", "DIRECT", "17.253.84.253"},
	}
	for i, s := range src {
		chain := []string{s[4]}
		if s[4] != "DIRECT" {
			chain = append(chain, f.resolve(s[4]))
		}
		netw := "tcp"
		if strings.HasSuffix(s[0], ":123") {
			netw = "udp"
		}
		f.conns = append(f.conns, model.Connection{ID: fmt.Sprintf("c%d", i+1), Host: s[0], DstIP: s[5], Network: netw, Type: "HTTP",
			Process: s[1], ProcessPath: "/Applications/" + s[1] + ".app", Rule: s[2], RulePayload: s[3], Chains: chain,
			SourceIP: "127.0.0.1", SourcePort: fmt.Sprint(52000 + i*7), RemoteDestination: "101.89.12.40",
			Download: int64(rand.Intn(8e6)), Upload: int64(rand.Intn(5e5)), Start: now.Add(-time.Duration(rand.Intn(900)) * time.Second)})
	}
	f.custom = []model.CustomRule{{ID: "r1", Type: "DOMAIN-SUFFIX", Payload: "openai.com", Target: "节点选择"}, {ID: "r2", Type: "PROCESS-NAME", Payload: "Steam", Target: "DIRECT"}}
	subRules := [][3]string{{"DOMAIN-SUFFIX", "local", "DIRECT"}, {"IP-CIDR", "192.168.0.0/16", "DIRECT"}, {"GEOSITE", "private", "DIRECT"},
		{"GEOSITE", "category-ads-all", "REJECT"}, {"DOMAIN-SUFFIX", "google.com", "节点选择"}, {"GEOSITE", "youtube", "流媒体"},
		{"GEOSITE", "github", "节点选择"}, {"DOMAIN-SUFFIX", "slack.com", "节点选择"}, {"IP-CIDR", "149.154.160.0/20", "节点选择"},
		{"GEOSITE", "cn", "DIRECT"}, {"GEOIP", "CN", "DIRECT"}, {"MATCH", "", "节点选择"}}
	f.rebuildRules(subRules)
	f.provs = []model.RuleProvider{{Name: "anti-ad", Behavior: "domain", Format: "mrs", Count: 82341, VehicleType: "HTTP", UpdatedAt: now.Add(-26 * time.Hour)},
		{Name: "telegram-cidr", Behavior: "ipcidr", Format: "yaml", Count: 18, VehicleType: "HTTP", UpdatedAt: now.Add(-72 * time.Hour)}}
	f.set = model.Settings{CloseToTray: true, Appearance: "system", Language: "zh", MixedPort: 7890, Bypass: []string{"localhost", "127.0.0.1", "192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "*.local"},
		TUNStack: "mixed", ProbeExits: true, Mode: model.ModeRule, GlobalShortcut: "CmdOrCtrl+Shift+S", LogLevel: "info",
		DNS: model.DNSSettings{EnhancedMode: "fake-ip", Nameserver: []string{"https://doh.pub/dns-query", "https://dns.alidns.com/dns-query"}, ProxyServerNameserver: []string{"https://doh.pub/dns-query"}, DirectNameserver: []string{"system"},
			Policy: []model.KV{{Key: "geosite:geolocation-!cn", Value: "https://1.1.1.1/dns-query"}}, FakeIPFilter: []string{"*.lan", "*.local", "+.msftconnecttest.com"}, UseSystemHosts: true},
		Sniffer: model.SnifferSettings{Enable: true, HTTP: true, TLS: true, HTTPPorts: "80, 8080-8880", TLSPorts: "443, 8443", QUICPorts: "443, 8443", OverrideDestination: true},
		Geo:     model.GeoSettings{GeoIP: geoURL + "geoip.dat", GeoSite: geoURL + "geosite.dat", MMDB: geoURL + "geoip.metadb", ASN: geoURL + "GeoLite2-ASN.mmdb", Mode: "db", IntervalHours: 24, UpdatedAt: now.Add(-7 * 24 * time.Hour)},
		Sites:   []model.Site{{Name: "Google", URL: "https://www.google.com"}, {Name: "YouTube", URL: "https://www.youtube.com"}, {Name: "GitHub", URL: "https://github.com"}, {Name: "OpenAI", URL: "https://api.openai.com"}, {Name: "Cloudflare", URL: "https://www.cloudflare.com"}, {Name: "Apple", URL: "https://www.apple.com"}, {Name: "百度", URL: "https://www.baidu.com"}}}
	f.set.DelayURL, f.set.DelayTimeoutMs = "https://www.gstatic.com/generate_204", 5000
	f.ovl = []model.Override{
		{ID: "global", Name: "全局覆写", Enabled: true, Global: true, Source: model.OverrideLocal, Text: "# 在订阅规则前插入\n+rules:\n  - DOMAIN-SUFFIX,example.com,DIRECT\n\nlog-level: warning\n", UpdatedAt: now.Add(-3 * 24 * time.Hour)},
		{ID: "o2", Name: "去广告规则", Enabled: true, Source: model.OverrideURL, URL: "https://example.com/overrides/adblock.yaml", Text: "+rules:\n  - GEOSITE,category-ads-all,REJECT\n", UpdatedAt: now.Add(-6 * time.Hour)},
		{ID: "o3", Name: "实验：IPv6 DNS", Enabled: false, Source: model.OverrideLocal, Text: "dns:\n  ipv6: true\n", UpdatedAt: now.Add(-20 * 24 * time.Hour)},
	}
	f.subs[0].Overrides = []string{"o2"}
	f.subs[0].HomePage = "https://sub.example.com"
	f.px.Groups[0].Icon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><circle cx="8" cy="8" r="7" fill="#3b82f6"/><path d="M4 8h8M8 4v8" stroke="#fff" stroke-width="1.6" stroke-linecap="round"/></svg>`
	f.px.Groups[3].Icon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><rect x="2" y="3" width="12" height="10" rx="2" fill="#ef4444"/><path d="M7 6l3 2-3 2z" fill="#fff"/></svg>`
	px := []int{96, 104, 88, 131, 79, 142, 188}
	dr := []int{-1, -1, 412, -1, 36, 24, 18}
	for i, s := range f.set.Sites {
		f.sites = append(f.sites, model.SiteCheck{Name: s.Name, URL: s.URL, ViaProxy: px[i], Direct: dr[i]})
	}
	for i := 0; i < 40; i++ {
		f.logs = append(f.logs, f.genLog(now.Add(-time.Duration(40-i)*time.Second)))
		f.logTotal++
	}
	return f
}

const geoURL = "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/"

func (f *Fake) rebuildRules(sub [][3]string) {
	f.rules = nil
	for _, c := range f.custom {
		f.rules = append(f.rules, model.Rule{Index: len(f.rules), Type: c.Type, Payload: c.Payload, Proxy: c.Target, Size: -1, Custom: true, HitCount: int64(rand.Intn(200)), HitAt: time.Now().Add(-time.Minute)})
	}
	for _, r := range sub {
		size := -1
		if r[0] == "GEOSITE" {
			size = 1000 + rand.Intn(100000)
		}
		f.rules = append(f.rules, model.Rule{Index: len(f.rules), Type: r[0], Payload: r[1], Proxy: r[2], Size: size, HitCount: int64(rand.Intn(900)), HitAt: time.Now().Add(-time.Duration(rand.Intn(3600)) * time.Second)})
	}
}

func (f *Fake) resolve(name string) string {
	for i := 0; i < 6; i++ {
		found := false
		for _, g := range f.px.Groups {
			if g.Name == name {
				name, found = g.Now, true
				break
			}
		}
		if !found {
			break
		}
	}
	return name
}

func (f *Fake) genLog(t time.Time) model.LogEntry {
	r := rand.Float64()
	switch {
	case r < .05:
		return model.LogEntry{Time: t, Level: "error", Payload: "proxy 美国 05 health check failed: context deadline exceeded"}
	case r < .15:
		return model.LogEntry{Time: t, Level: "warning", Payload: "[DNS] resolve api.example.com failed: i/o timeout"}
	default:
		c := f.conns[rand.Intn(len(f.conns))]
		return model.LogEntry{Time: t, Level: "info", Payload: fmt.Sprintf("[TCP] 127.0.0.1:%s --> %s match %s(%s) using %s", c.SourcePort, c.Host, c.Rule, c.RulePayload, strings.Join(c.Chains, "[")+"]")}
	}
}

func (f *Fake) loop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-f.stop:
			return
		case <-t.C:
		}
		f.Tick()
	}
}

// SetPendingRecovery makes Status report a leftover system proxy waiting
// for the user's answer (tests of the recovery prompt).
func (f *Fake) SetPendingRecovery(on bool) {
	f.mu.Lock()
	f.st.PendingRecovery = on
	f.mu.Unlock()
	f.emit(app.TopicStatus)
}

// Tick runs one second of the simulation: traffic, connection counters
// and, mostly, a new log line (New ticks by itself; tests call it).
func (f *Fake) Tick() {
	f.mu.Lock()
	if f.st.Core == model.CoreRunning {
		u, d := int64(5e3+rand.Intn(60e3)), int64(100e3+rand.Intn(2500e3))
		f.up, f.down = append(f.up[1:], u), append(f.down[1:], d)
		f.st.Up, f.st.Down = u, d
		f.st.TotalUp += u
		f.st.TotalDown += d
		for i := range f.conns {
			c := &f.conns[i]
			if rand.Float64() < .4 {
				c.DownSpeed, c.UpSpeed = int64(rand.Intn(300e3)), int64(rand.Intn(8e3))
			} else {
				c.DownSpeed, c.UpSpeed = 0, 0
			}
			c.Download += c.DownSpeed
			c.Upload += c.UpSpeed
		}
		f.st.Connections = len(f.conns)
		if rand.Float64() < .7 {
			f.logs = append(f.logs, f.genLog(time.Now()))
			f.logTotal++
			if len(f.logs) > 1000 {
				f.logs = f.logs[1:]
			}
		}
	}
	f.mu.Unlock()
	f.emit(app.TopicTraffic, app.TopicStatus, app.TopicConns, app.TopicLogs, app.TopicExits)
}

func (f *Fake) emit(ts ...app.Topic) {
	f.mu.Lock()
	fns := make([]func(app.Topic), 0, len(f.subsFns))
	for _, fn := range f.subsFns {
		fns = append(fns, fn)
	}
	f.mu.Unlock()
	for _, t := range ts {
		for _, fn := range fns {
			fn(t)
		}
	}
}

func (f *Fake) wait() { time.Sleep(f.Delay) }

// ---- reads ----

func (f *Fake) Status() model.Status { f.mu.Lock(); defer f.mu.Unlock(); return f.st }
func (f *Fake) TrafficHistory() ([]int64, []int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.up...), append([]int64(nil), f.down...)
}
func (f *Fake) Proxies() model.ProxySnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.px
	s.Groups = append([]model.ProxyGroup(nil), f.px.Groups...)
	s.Proxies = make(map[string]model.Proxy, len(f.px.Proxies))
	for k, v := range f.px.Proxies {
		s.Proxies[k] = v
	}
	return s
}
func (f *Fake) Subscriptions() []model.Subscription {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Subscription(nil), f.subs...)
}
func (f *Fake) Connections() []model.Connection {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.st.Core != model.CoreRunning {
		return nil
	}
	return append([]model.Connection(nil), f.conns...)
}
func (f *Fake) ClosedConnections() []model.Connection {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Connection(nil), f.closed...)
}
func (f *Fake) Logs() []model.LogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.LogEntry(nil), f.logs...)
}
func (f *Fake) Rules() []model.Rule {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Rule(nil), f.rules...)
}
func (f *Fake) CustomRules() []model.CustomRule {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.CustomRule(nil), f.custom...)
}
func (f *Fake) RuleProviders() []model.RuleProvider {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.RuleProvider(nil), f.provs...)
}
func (f *Fake) ProxyProviders() []model.ProxyProvider {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]model.ProxyProvider(nil), f.pprovs...)
	for i := range out {
		out[i].Proxies = append([]string(nil), out[i].Proxies...)
	}
	return out
}
func (f *Fake) UIState() model.UIState {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.set.UI
	u.OpenGroups = make(map[string]bool, len(f.set.UI.OpenGroups))
	for k, v := range f.set.UI.OpenGroups {
		u.OpenGroups[k] = v
	}
	return u
}
func (f *Fake) SaveUIState(u model.UIState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set.UI = u
	return nil
}
func (f *Fake) UpdateUIState(fn func(*model.UIState)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.set.UI
	u.OpenGroups = make(map[string]bool, len(f.set.UI.OpenGroups))
	for k, v := range f.set.UI.OpenGroups {
		u.OpenGroups[k] = v
	}
	fn(&u)
	f.set.UI = u
	return nil
}

// DataStrings returns the Chinese texts the fake fabricates as data: node,
// group, provider and subscription names, places, ISPs and notes. The
// English UI test removes them from rendered texts before looking for
// Chinese that the catalog missed, since names are never translated.
func (f *Fake) DataStrings() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), nodes...)
	for _, g := range f.px.Groups {
		out = append(out, g.Name)
	}
	for _, p := range f.pprovs {
		out = append(out, p.Name)
	}
	for _, s := range f.subs {
		out = append(out, s.Name, s.LastError)
	}
	for _, p := range places {
		out = append(out, p.City)
	}
	for _, site := range f.set.Sites {
		out = append(out, site.Name)
	}
	for _, o := range f.ovl {
		out = append(out, o.Name)
	}
	out = append(out, "中国电信", "名称为台湾，实测出口在香港")
	return out
}

var places = map[string]model.Place{
	"东京": {Country: "JP", City: "东京", Lat: 35.68, Lon: 139.69}, "香港": {Country: "HK", City: "香港", Lat: 22.3, Lon: 114.17},
	"新加坡": {Country: "SG", City: "新加坡", Lat: 1.35, Lon: 103.82}, "洛杉矶": {Country: "US", City: "洛杉矶", Lat: 34.05, Lon: -118.24},
	"圣何塞": {Country: "US", City: "圣何塞", Lat: 37.34, Lon: -121.89}, "伦敦": {Country: "GB", City: "伦敦", Lat: 51.51, Lon: -0.13},
	"上海": {Country: "CN", City: "上海", Lat: 31.23, Lon: 121.47},
}

func exitOf(node string) model.ExitInfo {
	city := map[string]string{"香港 01": "香港", "香港 02": "香港", "东京 03": "东京", "东京 04": "东京", "新加坡 02": "新加坡", "台湾 01": "香港", "美国 05": "洛杉矶", "美国 06": "圣何塞", "英国 01": "伦敦"}[node]
	p, ok := places[city]
	if !ok {
		return model.ExitInfo{Node: node, Source: model.LocateUnknown}
	}
	sh := places["上海"]
	e := model.ExitInfo{Node: node, Place: &p, Source: model.LocateProbe, Exit: model.EndpointInfo{IP: "45.76.211.38", Place: &p, ISP: "Vultr", ASN: "AS20473"}, Colo: "NRT",
		Entry: &model.EndpointInfo{IP: "101.89.12.40", Place: &sh, ISP: "中国电信", ASN: "AS4812"}, Path: model.PathRelay, MeasuredAt: time.Now().Add(-time.Hour)}
	if node == "台湾 01" {
		e.Path, e.PathNote, e.Entry = model.PathSuspicious, "名称为台湾，实测出口在香港", nil
	}
	if strings.HasPrefix(node, "英国") || strings.HasPrefix(node, "新加坡") {
		e.Source = model.LocateName
	}
	return e
}

func (f *Fake) ExitRoutes() []model.ExitRoute {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.st.Core != model.CoreRunning || f.st.Mode == model.ModeDirect {
		return nil
	}
	main := f.px.MainGroup
	if f.st.Mode == model.ModeGlobal {
		main = "GLOBAL"
	}
	idx := map[string]int{}
	var rs []model.ExitRoute
	add := func(g string) *model.ExitRoute {
		if i, ok := idx[g]; ok {
			return &rs[i]
		}
		n := f.resolve(g)
		idx[g] = len(rs)
		rs = append(rs, model.ExitRoute{Group: g, Node: n, Exit: exitOf(n), Main: g == main})
		return &rs[len(rs)-1]
	}
	add(main)
	for _, c := range f.conns {
		g := c.Chains[0]
		if g == "DIRECT" {
			continue
		}
		if f.st.Mode == model.ModeGlobal {
			g = "GLOBAL"
		}
		r := add(g)
		r.Speed += c.DownSpeed + c.UpSpeed
		r.Connections++
	}
	return rs
}
func (f *Fake) You() *model.EndpointInfo {
	p := places["上海"]
	return &model.EndpointInfo{IP: "116.227.38.12", Place: &p, ISP: "中国电信", ASN: "AS4812", BoundIface: "en0"}
}
func (f *Fake) NetInfo() model.NetInfo {
	return model.NetInfo{Interface: "Wi-Fi (en0)", SSID: "Home-5G", LANIP: "192.168.1.23", Gateway: "192.168.1.1", DNS: []string{"192.168.1.1"}, PublicIPStatus: "ok"}
}
func (f *Fake) Sites() []model.SiteCheck {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.SiteCheck(nil), f.sites...)
}
func (f *Fake) Settings() model.Settings { f.mu.Lock(); defer f.mu.Unlock(); return f.set }
func (f *Fake) Overrides() model.Overrides {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.overridesLocked()
}

// overridesLocked derives the legacy views from the list; caller holds mu.
func (f *Fake) overridesLocked() model.Overrides {
	ov := model.Overrides{PerSub: map[string]string{}, List: append([]model.Override(nil), f.ovl...)}
	for _, o := range f.ovl {
		if o.ID == "global" {
			ov.Global = o.Text
		} else if f.sub(o.ID) != nil {
			ov.PerSub[o.ID] = o.Text
		}
	}
	return ov
}

func (f *Fake) OverrideList() []model.Override {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Override(nil), f.ovl...)
}

// findOverride returns the entry's position, -1 if absent; caller holds mu.
func (f *Fake) findOverride(id string) int {
	for i, o := range f.ovl {
		if o.ID == id {
			return i
		}
	}
	return -1
}

func (f *Fake) SaveOverrideEntry(o model.Override) (model.Override, error) {
	o.Name = strings.TrimSpace(o.Name)
	if o.Name == "" {
		return o, errors.New(i18n.T("请填写覆写名称。"))
	}
	if strings.Contains(o.Text, "\t") {
		return o, errors.New(i18n.T("第 2 行：YAML 不允许使用 Tab 缩进。当前配置未改变"))
	}
	if o.Source == model.OverrideURL {
		if !strings.HasPrefix(o.URL, "http://") && !strings.HasPrefix(o.URL, "https://") {
			return o, errors.New(i18n.T("覆写链接无效。请输入以 http:// 或 https:// 开头的完整链接。"))
		}
		if strings.TrimSpace(o.Text) == "" {
			f.wait()
			o.Text = "# downloaded from " + o.URL + "\nlog-level: info\n"
		}
	} else {
		o.Source, o.URL = model.OverrideLocal, ""
	}
	err := f.do(func() error {
		if o.ID == "" {
			o.ID = fmt.Sprintf("o%d", len(f.ovl)+10)
		}
		o.UpdatedAt = time.Now()
		if i := f.findOverride(o.ID); i >= 0 {
			f.ovl[i] = o
		} else {
			f.ovl = append(f.ovl, o)
		}
		return nil
	}, app.TopicSettings)
	return o, err
}

func (f *Fake) DeleteOverrideEntry(id string) error {
	return f.do(func() error {
		if i := f.findOverride(id); i >= 0 {
			f.ovl = append(f.ovl[:i], f.ovl[i+1:]...)
		}
		for k := range f.subs {
			var keep []string
			for _, x := range f.subs[k].Overrides {
				if x != id {
					keep = append(keep, x)
				}
			}
			f.subs[k].Overrides = keep
		}
		return nil
	}, app.TopicSettings, app.TopicSubs)
}

func (f *Fake) MoveOverrideEntry(id string, delta int) error {
	return f.do(func() error {
		i := f.findOverride(id)
		if i < 0 {
			return errors.New(i18n.T("覆写不存在，可能已被删除。"))
		}
		j := min(max(i+delta, 0), len(f.ovl)-1)
		o := f.ovl[i]
		f.ovl = append(f.ovl[:i], f.ovl[i+1:]...)
		f.ovl = append(f.ovl[:j], append([]model.Override{o}, f.ovl[j:]...)...)
		return nil
	}, app.TopicSettings)
}

func (f *Fake) UpdateOverrideEntry(id string) error {
	return f.do(func() error {
		i := f.findOverride(id)
		if i < 0 {
			return errors.New(i18n.T("覆写不存在，可能已被删除。"))
		}
		if f.ovl[i].Source != model.OverrideURL {
			return errors.New(i18n.T("这条覆写不是从链接导入的，无法更新。"))
		}
		f.ovl[i].UpdatedAt = time.Now()
		return nil
	}, app.TopicSettings)
}

// IconData decodes inline SVG and data: URLs; URLs are never fetched here.
func (f *Fake) IconData(ref string) ([]byte, bool) {
	ref = strings.TrimSpace(ref)
	switch {
	case strings.HasPrefix(ref, "<svg"):
		return []byte(ref), true
	case strings.HasPrefix(ref, "data:"):
		_, payload, ok := strings.Cut(ref, ",")
		if !ok {
			return nil, false
		}
		if d, err := base64.StdEncoding.DecodeString(payload); err == nil {
			return d, true
		}
	}
	return nil, false
}

// PushNotice queues a notice as the service would (tests of the shell's
// notice handling).
func (f *Fake) PushNotice(n app.Notice) {
	f.mu.Lock()
	f.notices = append(f.notices, n)
	f.mu.Unlock()
	f.emit(app.TopicNotice)
}

func (f *Fake) TakeNotices() []app.Notice {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.notices
	f.notices = nil
	return n
}
func (f *Fake) ConfigDir() string { return "~/Library/Application Support/Lanflow" }
func (f *Fake) LogDir() string    { return "~/Library/Logs/Lanflow" }
func (f *Fake) OnChange(fn func(app.Topic)) func() {
	f.mu.Lock()
	id := f.nextFn
	f.nextFn++
	f.subsFns[id] = fn
	f.mu.Unlock()
	return func() { f.mu.Lock(); delete(f.subsFns, id); f.mu.Unlock() }
}

// ---- actions ----

func (f *Fake) do(fn func() error, ts ...app.Topic) error {
	f.wait()
	f.mu.Lock()
	err := fn()
	f.mu.Unlock()
	f.emit(append(ts, app.TopicStatus)...)
	return err
}

func (f *Fake) StartCore() error {
	f.mu.Lock()
	f.st.Core = model.CoreStarting
	f.mu.Unlock()
	f.emit(app.TopicStatus)
	return f.do(func() error {
		f.st.Core, f.st.StartedAt, f.st.CoreError = model.CoreRunning, time.Now(), ""
		return nil
	}, app.TopicExits, app.TopicConns)
}
func (f *Fake) StopCore() error {
	return f.do(func() error { f.st.Core, f.st.SystemProxy, f.st.TUN = model.CoreStopped, false, false; return nil }, app.TopicExits, app.TopicConns)
}
func (f *Fake) RestartCore() error { _ = f.StopCore(); return f.StartCore() }
func (f *Fake) SetSystemProxy(on bool) error {
	if on && f.Status().Core != model.CoreRunning {
		if err := f.StartCore(); err != nil {
			return err
		}
	}
	return f.do(func() error { f.st.SystemProxy = on; return nil })
}
func (f *Fake) SetTUN(on bool) error {
	if on && !f.TUNHelperInstalled() {
		return app.ErrHelperRequired
	}
	return f.do(func() error { f.st.TUN = on; f.set.TUN = on; return nil }, app.TopicSettings)
}
func (f *Fake) TUNHelperInstalled() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.helper }
func (f *Fake) InstallTUNHelper() error {
	return f.do(func() error { f.helper, f.st.TUNAvailable = true, true; return nil })
}
func (f *Fake) UninstallTUNHelper() error {
	return f.do(func() error { f.helper, f.st.TUNAvailable, f.st.TUN = false, false, false; return nil })
}
func (f *Fake) SetMode(m model.Mode) error {
	return f.do(func() error { f.st.Mode, f.set.Mode = m, m; return nil }, app.TopicExits, app.TopicProxies)
}
func (f *Fake) ResolveRecovery(bool) error {
	return f.do(func() error { f.st.PendingRecovery = false; return nil })
}

func (f *Fake) SelectProxy(group, name string) error {
	return f.do(func() error {
		for i, g := range f.px.Groups {
			if g.Name == group {
				if g.Type != model.GroupSelect {
					return errors.New(i18n.T("该代理组不能手动选择"))
				}
				f.px.Groups[i].Now = name
				if f.set.DisconnectOnSwitch {
					kept := f.conns[:0]
					for _, c := range f.conns {
						if !slices.Contains(c.Chains, group) {
							kept = append(kept, c)
						}
					}
					f.conns = kept
				}
				return nil
			}
		}
		return fmt.Errorf(i18n.T("找不到代理组「%s」"), group)
	}, app.TopicProxies, app.TopicExits, app.TopicConns)
}
func (f *Fake) testNames(names []string) error {
	f.mu.Lock()
	for _, n := range names {
		if p, ok := f.px.Proxies[n]; ok && p.Type != "Selector" {
			p.Testing = true
			f.px.Proxies[n] = p
		}
	}
	f.mu.Unlock()
	f.emit(app.TopicProxies)
	// Results arrive one by one, as the real service reports them.
	for _, n := range names {
		time.Sleep(f.Delay / 3)
		f.mu.Lock()
		p, ok := f.px.Proxies[n]
		if !ok || !p.Testing {
			f.mu.Unlock()
			continue
		}
		p.Testing = false
		switch r := rand.Float64(); {
		case r < .08:
			p.Delay = -1
		case r < .2:
			p.Delay = 320 + rand.Intn(780)
		default:
			p.Delay = 45 + rand.Intn(255)
		}
		f.px.Proxies[n] = p
		f.mu.Unlock()
		f.emit(app.TopicProxies)
	}
	f.emit(app.TopicProxies, app.TopicStatus)
	return nil
}
func (f *Fake) TestGroupDelay(group string) error {
	for _, g := range f.Proxies().Groups {
		if g.Name == group {
			return f.testNames(g.All)
		}
	}
	return nil
}
func (f *Fake) TestProxyDelay(name string) error { return f.testNames([]string{name}) }
func (f *Fake) TestAllDelays() error             { return f.testNames(nodes) }
func (f *Fake) pprov(name string) *model.ProxyProvider {
	for i := range f.pprovs {
		if f.pprovs[i].Name == name {
			return &f.pprovs[i]
		}
	}
	return nil
}
func (f *Fake) UpdateProxyProvider(name string) error {
	f.mu.Lock()
	p := f.pprov(name)
	if p == nil {
		f.mu.Unlock()
		return fmt.Errorf(i18n.T("没有找到代理集合「%s」"), name)
	}
	p.Updating = true
	f.mu.Unlock()
	f.emit(app.TopicProxies)
	return f.do(func() error {
		p := f.pprov(name)
		p.Updating, p.UpdatedAt = false, time.Now()
		return nil
	}, app.TopicProxies)
}
func (f *Fake) HealthCheckProvider(name string) error {
	f.mu.Lock()
	p := f.pprov(name)
	if p == nil {
		f.mu.Unlock()
		return fmt.Errorf(i18n.T("没有找到代理集合「%s」"), name)
	}
	p.Checking = true
	members := append([]string(nil), p.Proxies...)
	f.mu.Unlock()
	f.emit(app.TopicProxies)
	err := f.testNames(members)
	f.mu.Lock()
	if p := f.pprov(name); p != nil {
		p.Checking = false
	}
	f.mu.Unlock()
	f.emit(app.TopicProxies)
	return err
}
func (f *Fake) SetMainGroup(group string) error {
	return f.do(func() error { f.px.MainGroup, f.set.MainGroup = group, group; return nil }, app.TopicProxies, app.TopicExits)
}

// setImportPhase publishes Status.ImportPhase (下载 → 校验 → 完成).
func (f *Fake) setImportPhase(phase string) {
	f.mu.Lock()
	f.st.ImportPhase = phase
	f.mu.Unlock()
	f.emit(app.TopicStatus)
}

func (f *Fake) ImportSubscriptionURL(url, name, ua string, viaProxy, use bool) (model.Subscription, error) {
	var s model.Subscription
	f.setImportPhase(model.ImportDownloading)
	defer f.setImportPhase("")
	err := f.do(func() error {
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			return errors.New(i18n.T("请输入以 http:// 或 https:// 开头的订阅链接"))
		}
		if strings.Contains(url, "fail") {
			return errors.New(i18n.T("下载失败：HTTP 403。链接可能已失效，请在机场后台重新复制订阅链接"))
		}
		return nil
	})
	if err != nil {
		return s, err
	}
	f.setImportPhase(model.ImportValidating)
	err = f.do(func() error {
		if name == "" {
			name = fmt.Sprintf("新机场 %d", len(f.subs)+1)
		}
		s = model.Subscription{ID: fmt.Sprintf("s%d", len(f.subs)+10), Name: name, Source: "url", URL: url, UserAgent: ua, ViaProxy: viaProxy, Download: 1 << 30, Total: 100 << 30, Expire: time.Now().AddDate(1, 0, 0), UpdatedAt: time.Now(), AutoUpdateHours: 12}
		f.subs = append(f.subs, s)
		f.st.HasSubscriptions = true
		if use {
			f.setCurrent(s.ID)
		}
		return nil
	}, app.TopicSubs)
	return s, err
}
func (f *Fake) ImportSubscriptionFile(path string, use bool) (model.Subscription, error) {
	var s model.Subscription
	err := f.do(func() error {
		parts := strings.Split(path, "/")
		s = model.Subscription{ID: fmt.Sprintf("s%d", len(f.subs)+10), Name: parts[len(parts)-1], Source: "file", FilePath: path, UpdatedAt: time.Now()}
		f.subs = append(f.subs, s)
		f.st.HasSubscriptions = true
		if use {
			f.setCurrent(s.ID)
		}
		return nil
	}, app.TopicSubs)
	return s, err
}
func (f *Fake) setCurrent(id string) {
	for i := range f.subs {
		f.subs[i].Current = f.subs[i].ID == id
	}
	f.st.CurrentSubscription = id
}
func (f *Fake) sub(id string) *model.Subscription {
	for i := range f.subs {
		if f.subs[i].ID == id {
			return &f.subs[i]
		}
	}
	return nil
}
func (f *Fake) UpdateSubscription(id string) error {
	f.mu.Lock()
	if s := f.sub(id); s != nil {
		s.Updating = true
	}
	f.mu.Unlock()
	f.emit(app.TopicSubs)
	return f.do(func() error {
		s := f.sub(id)
		if s == nil {
			return errors.New(i18n.T("订阅不存在"))
		}
		s.Updating = false
		if s.ID == "s2" {
			s.LastError = "更新失败：连接超时。仍在使用上次的配置。"
			return errors.New(i18n.T("「备用机场」更新失败：连接超时"))
		}
		s.LastError, s.UpdatedAt = "", time.Now()
		return nil
	}, app.TopicSubs)
}
func (f *Fake) UpdateAllSubscriptions() error {
	var errs []string
	for _, s := range f.Subscriptions() {
		if s.Source == "url" {
			if err := f.UpdateSubscription(s.ID); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if errs != nil {
		return errors.New(strings.Join(errs, "；"))
	}
	return nil
}
func (f *Fake) UseSubscription(id string) error {
	return f.do(func() error {
		s := f.sub(id)
		if s == nil {
			return errors.New(i18n.T("订阅不存在"))
		}
		if s.ID == "s2" {
			f.notices = append(f.notices, app.Notice{Kind: "invalid-config", Title: i18n.T("配置无法使用"), Detail: "line 42: proxy-groups[1]: proxy 'xx' not found", Subject: id})
			return fmt.Errorf(i18n.T("「%s」的配置未通过校验"), s.Name)
		}
		f.setCurrent(id)
		return nil
	}, app.TopicSubs, app.TopicProxies, app.TopicRules, app.TopicNotice)
}
func (f *Fake) EditSubscription(n model.Subscription) error {
	return f.do(func() error {
		s := f.sub(n.ID)
		if s == nil {
			return errors.New(i18n.T("订阅不存在"))
		}
		s.Name, s.URL, s.AutoUpdateHours, s.UserAgent, s.ViaProxy = n.Name, n.URL, n.AutoUpdateHours, n.UserAgent, n.ViaProxy
		s.TimeoutSec, s.Overrides = n.TimeoutSec, append([]string(nil), n.Overrides...)
		return nil
	}, app.TopicSubs)
}
func (f *Fake) DeleteSubscription(id string) error {
	return f.do(func() error {
		for i, s := range f.subs {
			if s.ID == id {
				if s.Current {
					return errors.New(i18n.T("请先切换到其他订阅"))
				}
				f.subs = append(f.subs[:i], f.subs[i+1:]...)
				return nil
			}
		}
		return errors.New(i18n.T("订阅不存在"))
	}, app.TopicSubs)
}
func (f *Fake) SubscriptionRaw(id string) (string, error) {
	return "mixed-port: 7890\nproxies:\n  - name: 东京 03\n    type: trojan\n    server: jp3.example.com\n    port: 443\n", nil
}

func (f *Fake) CloseConnection(id string) error {
	return f.do(func() error {
		for i, c := range f.conns {
			if c.ID == id {
				c.ClosedAt = time.Now()
				f.closed = append([]model.Connection{c}, f.closed...)
				f.closedTotal++
				f.conns = append(f.conns[:i], f.conns[i+1:]...)
				return nil
			}
		}
		return nil
	}, app.TopicConns)
}
func (f *Fake) CloseAllConnections() error {
	return f.do(func() error {
		f.closed = append(f.conns, f.closed...)
		f.closedTotal += uint64(len(f.conns))
		f.conns = nil
		return nil
	}, app.TopicConns)
}
func (f *Fake) ClearLogs() {
	f.mu.Lock()
	f.logs = nil
	f.logTotal++
	f.logEpoch = f.logTotal
	f.mu.Unlock()
	f.emit(app.TopicLogs)
}

// AppLogs returns a few sample entries of Lanflow's own log.
func (f *Fake) AppLogs() []model.LogEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.appLogsCleared {
		return nil
	}
	now := time.Now()
	return []model.LogEntry{
		{Time: now.Add(-3 * time.Minute), Level: "info", Payload: "Lanflow starting (darwin/arm64)"},
		{Time: now.Add(-3 * time.Minute), Level: "info", Payload: "start core: ok"},
		{Time: now.Add(-2 * time.Minute), Level: "info", Payload: "system proxy on: ok"},
		{Time: now.Add(-time.Minute), Level: "error", Payload: "update subscription 「备用」: failed: 下载订阅超时"},
	}
}

// ClearAppLogs hides the sample entries.
func (f *Fake) ClearAppLogs() {
	f.mu.Lock()
	f.appLogsCleared = true
	f.mu.Unlock()
	f.emit(app.TopicLogs)
}

// ExportDiagnostics writes a small placeholder zip.
// CacheUsage and CleanCaches are the optional 「清理缓存」 side of the API
// (settings/caches.go); the fake reports made-up sizes until cleaned.
func (f *Fake) CacheUsage() []app.CacheItem {
	f.mu.Lock()
	defer f.mu.Unlock()
	items := []app.CacheItem{
		{Name: i18n.T("图标"), Bytes: 1_258_291, Files: 23},
		{Name: i18n.T("内核版本列表"), Bytes: 18_432, Files: 2},
		{Name: i18n.T("内核缓存"), Bytes: 131072, Files: 1},
		{Name: i18n.T("旧日志"), Bytes: 5 << 20, Files: 1},
	}
	if f.cachesCleaned {
		for i := range items {
			if items[i].Name != i18n.T("内核缓存") {
				items[i].Bytes, items[i].Files = 0, 0
			}
		}
	}
	return items
}

func (f *Fake) CleanCaches() (int64, error) {
	f.wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cachesCleaned {
		return 0, nil
	}
	f.cachesCleaned = true
	return 1_258_291 + 18_432 + 5<<20, nil
}

func (f *Fake) ExportDiagnostics(path string) error {
	f.wait()
	return os.WriteFile(path, []byte("PK\x05\x06"+strings.Repeat("\x00", 18)), 0o600)
}

func (f *Fake) SaveCustomRule(r model.CustomRule) (model.CustomRule, error) {
	err := f.do(func() error {
		if strings.TrimSpace(r.Payload) == "" {
			return errors.New(i18n.T("请填写规则内容"))
		}
		if r.ID == "" {
			r.ID = fmt.Sprintf("r%d", time.Now().UnixNano())
			f.custom = append([]model.CustomRule{r}, f.custom...)
		} else {
			for i := range f.custom {
				if f.custom[i].ID == r.ID {
					f.custom[i] = r
				}
			}
		}
		f.rebuildRules(nil)
		return nil
	}, app.TopicRules)
	return r, err
}
func (f *Fake) DeleteCustomRule(id string) error {
	return f.do(func() error {
		for i, r := range f.custom {
			if r.ID == id {
				f.custom = append(f.custom[:i], f.custom[i+1:]...)
				break
			}
		}
		return nil
	}, app.TopicRules)
}
func (f *Fake) MoveCustomRule(id string, delta int) error {
	return f.do(func() error {
		for i, r := range f.custom {
			if r.ID == id {
				j := i + delta
				if j >= 0 && j < len(f.custom) {
					f.custom[i], f.custom[j] = f.custom[j], f.custom[i]
				}
				break
			}
		}
		return nil
	}, app.TopicRules)
}
func (f *Fake) SetRuleDisabled(index int, disabled bool) error {
	return f.do(func() error {
		if index >= 0 && index < len(f.rules) {
			f.rules[index].Disabled = disabled
		}
		return nil
	}, app.TopicRules)
}
func (f *Fake) TestRule(host string) (model.RuleMatch, error) {
	f.wait()
	h := strings.ToLower(strings.TrimSpace(host))
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rules {
		if r.Disabled {
			continue
		}
		if (r.Type == "DOMAIN-SUFFIX" && (h == r.Payload || strings.HasSuffix(h, "."+r.Payload))) ||
			(r.Type == "GEOSITE" && strings.Contains(h, r.Payload)) || r.Type == "MATCH" {
			chain := []string{r.Proxy}
			if r.Proxy != "DIRECT" && r.Proxy != "REJECT" {
				chain = append(chain, f.resolve(r.Proxy))
			}
			return model.RuleMatch{Host: h, RuleIndex: r.Index, Rule: r.Type + "," + r.Payload, Custom: r.Custom, Policy: r.Proxy, Chain: chain}, nil
		}
	}
	return model.RuleMatch{Host: h, RuleIndex: -1}, nil
}
func (f *Fake) UpdateRuleProvider(name string) error {
	return f.do(func() error {
		for i := range f.provs {
			if f.provs[i].Name == name {
				f.provs[i].UpdatedAt = time.Now()
			}
		}
		return nil
	}, app.TopicRules)
}

func (f *Fake) ProbeExits() error     { return f.do(func() error { return nil }, app.TopicExits) }
func (f *Fake) RefreshNetwork() error { return f.do(func() error { return nil }, app.TopicNet) }

// LeakCheck returns the last 泄漏检查 result.
func (f *Fake) LeakCheck() model.LeakResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.leak
	out.Items = append([]model.LeakItem(nil), f.leak.Items...)
	return out
}

// RunLeakCheck simulates the check: in system-proxy mode without TUN the
// DNS, IPv6 and WebRTC items are partial, with TUN on they pass.
func (f *Fake) RunLeakCheck() error { return f.RunLeakCheckWith("") }

// RunLeakCheckWith is RunLeakCheck with a domain added to the DNS comparison.
func (f *Fake) RunLeakCheckWith(domain string) error {
	f.mu.Lock()
	f.leak.Checking = true
	tun := f.st.TUN
	f.mu.Unlock()
	f.emit(app.TopicNet)
	f.wait()
	var items []model.LeakItem
	if tun {
		items = []model.LeakItem{
			{Name: "DNS", Status: model.LeakOK, Summary: i18n.T("系统 DNS 请求经过澜流"), Detail: i18n.T("系统解析器返回的是澜流分配的 Fake-IP，说明应用的域名查询已由内核处理，不会直接发给运营商的 DNS。")},
			{Name: "IPv6", Status: model.LeakOK, Summary: i18n.T("本机没有可用的公网 IPv6"), Detail: i18n.T("没有 IPv6 连接，也就不存在 IPv6 流量绕过代理的问题。")},
			{Name: model.LeakItemWebRTC, Status: model.LeakOK, Summary: i18n.Tf("WebRTC（UDP）流量经过澜流，出口 %s", "45.76.211.38"), Detail: i18n.T("STUN 服务器看到的是代理出口地址，说明直接发出的 UDP 已被接管；浏览器的 WebRTC 不会暴露真实 IP。"), Rows: fakeLeakRTCRows("45.76.211.38")},
			fakeLeakIPType(),
			fakeLeakDNSCmp(domain, "198.18.0.7"),
		}
	} else {
		items = []model.LeakItem{
			{Name: "DNS", Status: model.LeakWarn, Summary: i18n.T("仅遵守系统代理的应用由澜流解析域名"), Detail: i18n.Tf("系统代理模式下，浏览器等遵守系统代理的应用把域名交给澜流处理，不会泄漏；不遵守系统代理的程序直接使用系统 DNS（%s）。要让所有应用的 DNS 请求经过澜流，请开启 TUN 模式。", "192.168.1.1")},
			{Name: "IPv6", Status: model.LeakOK, Summary: i18n.T("本机没有可用的公网 IPv6"), Detail: i18n.T("没有 IPv6 连接，也就不存在 IPv6 流量绕过代理的问题。")},
			{Name: model.LeakItemWebRTC, Status: model.LeakWarn, Summary: i18n.Tf("浏览器的 WebRTC 会暴露真实 IP（%s）", "116.227.38.12"), Detail: i18n.T("系统代理模式只覆盖 TCP 的 HTTP/HTTPS 请求，而浏览器的 WebRTC 直接用 UDP 向 STUN 服务器询问公网地址，网站可借此拿到真实 IP。要堵住这个口子，请开启 TUN 模式让 UDP 也经过澜流；或在浏览器中限制 WebRTC。"), Rows: fakeLeakRTCRows("116.227.38.12")},
			fakeLeakIPType(),
			fakeLeakDNSCmp(domain, "104.16.123.96"),
		}
	}
	f.mu.Lock()
	f.leak = model.LeakResult{Items: items, CheckedAt: time.Now()}
	f.mu.Unlock()
	f.emit(app.TopicNet)
	return nil
}

// fakeLeakRTCRows are the WebRTC item's rows with the STUN-mapped address.
func fakeLeakRTCRows(mapped string) []model.LeakRow {
	return []model.LeakRow{{Label: i18n.T("STUN 看到"), Value: mapped}, {Label: i18n.T("代理出口"), Value: "45.76.211.38"}, {Label: i18n.T("本机公网"), Value: "116.227.38.12"}}
}

// fakeLeakIPType is the sample IP 类型 item: a Vultr exit, a carrier at home.
func fakeLeakIPType() model.LeakItem {
	exit := "45.76.211.38 · AS20473 Vultr · " + i18n.T("机房 IP")
	direct := "116.227.38.12 · AS4812 中国电信 · " + i18n.T("家宽 / 运营商 IP")
	return model.LeakItem{Name: model.LeakItemIPType, Title: i18n.T("IP 类型"), Status: model.LeakOK, Summary: i18n.Tf("出口 %s", exit),
		Detail: i18n.T("出口属于云服务 / 机房网络，流媒体和风控系统容易把它识别为代理；需要更「纯净」的出口时可换用家宽或原生 IP 节点。"),
		Rows:   []model.LeakRow{{Label: i18n.T("代理出口"), Value: exit}, {Label: i18n.T("本机公网"), Value: direct}}}
}

// fakeLeakDNSCmp is the sample DNS 对比 item: three agreeing answers for
// the test host and, when given, the user's domain.
func fakeLeakDNSCmp(domain, sysAnswer string) model.LeakItem {
	rows := func(prefix, core, sys, pub string) []model.LeakRow {
		return []model.LeakRow{{Label: i18n.T("内核"), Value: prefix + core}, {Label: i18n.T("系统解析器"), Value: prefix + sys}, {Label: i18n.Tf("公共 DNS %s", "223.5.5.5"), Value: prefix + pub}}
	}
	item := model.LeakItem{Name: model.LeakItemDNSCmp, Title: i18n.T("DNS 对比"), Status: model.LeakOK, Summary: i18n.T("系统解析器的结果与内核或公共 DNS 一致"),
		Detail: i18n.T("三路解析相互印证，未发现劫持或污染。内核与公共 DNS 的结果不同属正常现象（内核可能经代理在境外解析）。")}
	if domain == "" {
		item.Rows = rows("", "104.16.123.96, 104.16.124.96", sysAnswer, "104.16.124.96, 104.16.123.96")
		return item
	}
	item.Rows = append(rows(i18n.Tf("%s：", "www.cloudflare.com"), "104.16.123.96, 104.16.124.96", sysAnswer, "104.16.124.96, 104.16.123.96"),
		rows(i18n.Tf("%s：", domain), "93.184.215.14", "93.184.215.14", "93.184.215.14")...)
	return item
}

// fakeSubDNS is the dns section of the sample subscription, shown
// read-only in 跟随订阅 mode.
var fakeSubDNS = model.DNSSettings{EnhancedMode: "fake-ip", Nameserver: []string{"223.5.5.5", "119.29.29.29"}, ProxyServerNameserver: []string{"https://dns.alidns.com/dns-query"},
	Policy: []model.KV{{Key: "geosite:cn", Value: "223.5.5.5"}, {Key: "geosite:geolocation-!cn", Value: "https://1.1.1.1/dns-query, https://8.8.8.8/dns-query"}}, FakeIPFilter: []string{"*.lan", "+.stun.*.*"}, UseSystemHosts: true}

// EffectiveDNS is Lanflow's DNS when taken over, else the sample
// subscription's dns section.
func (f *Fake) EffectiveDNS() model.EffectiveDNS {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.set.DNS.TakeOver {
		return model.EffectiveDNS{Settings: f.set.DNS, Source: model.EffectiveLanflow, Enabled: true}
	}
	return model.EffectiveDNS{Settings: fakeSubDNS, Source: model.EffectiveSubscription, Enabled: true, Listen: "0.0.0.0:1053"}
}

// EffectiveSniffer is Lanflow's sniffer when taken over, else the sample
// subscription's sniffer section (TLS only).
func (f *Fake) EffectiveSniffer() model.EffectiveSniffer {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.set.Sniffer.TakeOver {
		return model.EffectiveSniffer{Settings: f.set.Sniffer, Source: model.EffectiveLanflow}
	}
	return model.EffectiveSniffer{Settings: model.SnifferSettings{Enable: f.set.Sniffer.Enable, TLS: true, TLSPorts: "443", SkipDomain: []string{"Mijia Cloud"}}, Source: model.EffectiveSubscription}
}

// GeoDataInfo pretends the databases were downloaded three days ago.
func (f *Fake) GeoDataInfo() model.GeoDataInfo {
	return model.GeoDataInfo{UpdatedAt: time.Now().Add(-3 * 24 * time.Hour), Files: 4}
}

// fakeCountries maps the sample connections' destination IPs to countries.
var fakeCountries = map[string]string{
	"149.154.167.99": "NL", "35.186.224.25": "US", "104.18.22.110": "US", "162.159.128.233": "US",
	"120.92.78.97": "CN", "17.253.84.253": "US",
}

func (f *Fake) LookupIP(ip string) model.EndpointInfo {
	ep := model.EndpointInfo{IP: ip}
	code, ok := fakeCountries[strings.TrimSpace(ip)]
	if !ok {
		return ep
	}
	if p, ok := geo.Country(code); ok {
		p.City = geo.CountryName(code)
		ep.Place = &p
	}
	return ep
}
func (f *Fake) TestSites() error {
	f.mu.Lock()
	for i := range f.sites {
		f.sites[i].ViaProxy, f.sites[i].Direct = -2, -2
	}
	f.mu.Unlock()
	f.emit(app.TopicNet)
	return f.do(func() error {
		for i := range f.sites {
			f.sites[i].ViaProxy = 70 + rand.Intn(150)
			if i < 2 || i == 3 {
				f.sites[i].Direct = -1
			} else {
				f.sites[i].Direct = 12 + rand.Intn(400)
			}
		}
		return nil
	}, app.TopicNet)
}
func (f *Fake) DNSQuery(name, qtype string) (model.DNSResult, error) {
	f.wait()
	if strings.Contains(name, "baidu") {
		return model.DNSResult{Name: name, Type: qtype, Answers: []string{"110.242.68.3"}, Status: "NOERROR"}, nil
	}
	return model.DNSResult{Name: name, Type: qtype, Answers: []string{"142.250.196.68"}, Status: "NOERROR", FakeIP: true,
		AppAnswers: []string{fmt.Sprintf("198.18.0.%d", 2+rand.Intn(250))}, AppSource: "system"}, nil
}

// DismissProxyHint hides the first-run bubble next to the system proxy switch.
func (f *Fake) DismissProxyHint() error {
	f.mu.Lock()
	f.st.ShowProxyHint = false
	f.mu.Unlock()
	f.emit(app.TopicStatus)
	return nil
}
func (f *Fake) FlushDNSCache() error    { f.wait(); return nil }
func (f *Fake) FlushFakeIPCache() error { f.wait(); return nil }

func (f *Fake) SaveSettings(s model.Settings) error {
	return f.do(func() error {
		if s.MixedPort < 1 || s.MixedPort > 65535 {
			return errors.New(i18n.T("端口须为 1–65535 之间的数字"))
		}
		if s.MixedPort == 7891 {
			return errors.New(i18n.T("端口 7891 已被其他程序占用"))
		}
		f.set = s
		f.st.MixedPort, f.st.AllowLAN, f.st.Mode = s.MixedPort, s.AllowLAN, s.Mode
		i18n.SetLang(s.Language)
		return nil
	}, app.TopicSettings)
}
func (f *Fake) ApplyDNS(d model.DNSSettings) error {
	return f.do(func() error { f.set.DNS = d; return nil }, app.TopicSettings)
}
func (f *Fake) ApplySniffer(s model.SnifferSettings) error {
	return f.do(func() error { f.set.Sniffer = s; return nil }, app.TopicSettings)
}
func (f *Fake) SaveOverride(key, text string) error {
	return f.do(func() error {
		if strings.Contains(text, "\t") {
			return errors.New(i18n.T("第 2 行：YAML 不允许使用 Tab 缩进。当前配置未改变"))
		}
		i := f.findOverride(key)
		if text == "" {
			if i >= 0 {
				f.ovl = append(f.ovl[:i], f.ovl[i+1:]...)
			}
			return nil
		}
		if i >= 0 {
			f.ovl[i].Text, f.ovl[i].UpdatedAt = text, time.Now()
			return nil
		}
		o := model.Override{ID: key, Name: key, Enabled: true, Source: model.OverrideLocal, Text: text, UpdatedAt: time.Now()}
		if key == "global" {
			o.Name, o.Global = i18n.T("全局覆写"), true
		} else if sub := f.sub(key); sub != nil {
			o.Name = sub.Name + i18n.T(" 覆写")
			sub.Overrides = append(sub.Overrides, key)
		}
		f.ovl = append(f.ovl, o)
		return nil
	}, app.TopicSettings)
}
func (f *Fake) PreviewOverride(key, text string) ([]model.DiffLine, error) {
	f.wait()
	var out []model.DiffLine
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") {
			continue
		}
		out = append(out, model.DiffLine{Op: '+', Text: l})
	}
	return out, nil
}
func (f *Fake) RuntimeConfig() ([]model.RuntimeLine, error) {
	return []model.RuntimeLine{{Text: "mixed-port: 7890", Source: i18n.T("澜流设置")}, {Text: "mode: rule", Source: i18n.T("澜流设置")}, {Text: "log-level: warning", Source: i18n.T("全局覆写")},
		{Text: "dns:", Source: i18n.T("订阅")}, {Text: "  enhanced-mode: fake-ip", Source: ""}, {Text: "proxy-groups:", Source: i18n.T("订阅")}, {Text: "  - name: 节点选择", Source: ""},
		{Text: "rules:", Source: ""}, {Text: "  - DOMAIN-SUFFIX,openai.com,节点选择", Source: i18n.T("自定义规则")}, {Text: "  - DOMAIN-SUFFIX,example.com,DIRECT", Source: i18n.T("全局覆写")}, {Text: "  - DOMAIN-SUFFIX,local,DIRECT", Source: i18n.T("订阅")}}, nil
}
func (f *Fake) UpdateGeoData() error {
	return f.do(func() error { f.set.Geo.UpdatedAt = time.Now(); return nil }, app.TopicSettings)
}

// ---- core versions ----

// fakeReleases are the sample release lists of both channels.
var fakeReleases = map[string][]model.CoreRelease{
	model.CoreChannelStable: {
		{Channel: model.CoreChannelStable, Version: "v1.19.34", Tag: "v1.19.34", PublishedAt: time.Now().Add(-3 * 24 * time.Hour), Notes: "fix: DNS fallback filter\nfeat: new sniffer options\nchore: dependencies", Asset: "mihomo-darwin-arm64-v1.19.34.gz", Size: 19 << 20},
		{Channel: model.CoreChannelStable, Version: "v1.19.33", Tag: "v1.19.33", PublishedAt: time.Now().Add(-20 * 24 * time.Hour), Notes: "fix: TUN on macOS 26\nfeat: REALITY improvements", Asset: "mihomo-darwin-arm64-v1.19.33.gz", Size: 19 << 20},
		{Channel: model.CoreChannelStable, Version: "v1.19.32", Tag: "v1.19.32", PublishedAt: time.Now().Add(-45 * 24 * time.Hour), Notes: "fix: fake-ip pool exhaustion", Asset: "mihomo-darwin-arm64-v1.19.32.gz", Size: 18 << 20},
	},
	model.CoreChannelAlpha: {
		{Channel: model.CoreChannelAlpha, Version: "alpha-5a3a0ef", Tag: "Prerelease-Alpha", PublishedAt: time.Now().Add(-6 * time.Hour), Notes: "Alpha build of the latest commit", Asset: "mihomo-darwin-arm64-alpha-5a3a0ef.gz", Size: 19 << 20},
	},
}

// coresLocked fills the sample list on first use; f.mu must be held.
func (f *Fake) coresLocked() []model.InstalledCore {
	if f.cores == nil {
		f.cores = []model.InstalledCore{
			{Version: "v1.19.33", Bundled: true, Active: f.set.CoreVersion == "", Path: "/Applications/Lanflow.app/Contents/Resources/mihomo", Size: 46 << 20, InstalledAt: time.Now().Add(-30 * 24 * time.Hour), Source: "bundled"},
		}
	}
	for i := range f.cores {
		f.cores[i].Active = f.cores[i].Bundled && f.set.CoreVersion == "" || !f.cores[i].Bundled && f.cores[i].Version == f.set.CoreVersion
	}
	return f.cores
}

func (f *Fake) CoreReleases(channel string, refresh bool) ([]model.CoreRelease, error) {
	f.wait()
	if channel == "" {
		channel = model.CoreChannelStable
	}
	list, ok := fakeReleases[channel]
	if !ok {
		return nil, fmt.Errorf(i18n.T("未知的内核更新通道：%s"), channel)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.CoreRelease, len(list))
	copy(out, list)
	for i := range out {
		for _, c := range f.coresLocked() {
			if !c.Bundled && c.Version == out[i].Version {
				out[i].Installed = true
			}
		}
	}
	return out, nil
}

func (f *Fake) InstalledCores() []model.InstalledCore {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.InstalledCore(nil), f.coresLocked()...)
}

// InstallCore simulates a download in a few steps and adds the version.
func (f *Fake) InstallCore(channel, version string) error {
	if channel == "" {
		channel = model.CoreChannelStable
	}
	list := fakeReleases[channel]
	if version == "" && len(list) > 0 {
		version = list[0].Version
	}
	if version == "v9.9.9" {
		return errors.New(i18n.T("GitHub 上没有这个版本的内核（HTTP 404）"))
	}
	const total = int64(18 << 20)
	f.mu.Lock()
	if f.coreDL.Active {
		f.mu.Unlock()
		return errors.New(i18n.T("内核正在下载中，请稍候。"))
	}
	f.coreDL = model.CoreDownload{Active: true, Phase: "下载", Version: version, Total: total}
	f.mu.Unlock()
	f.emit(app.TopicCoreDownload)
	for i := 1; i <= 8; i++ {
		f.wait()
		f.mu.Lock()
		f.coreDL.Bytes = total * int64(i) / 8
		f.mu.Unlock()
		f.emit(app.TopicCoreDownload)
	}
	for _, ph := range []string{"校验", "安装"} {
		f.mu.Lock()
		f.coreDL.Phase = ph
		f.mu.Unlock()
		f.emit(app.TopicCoreDownload)
		f.wait()
	}
	f.mu.Lock()
	f.coreDL = model.CoreDownload{Done: version, Bytes: total, Total: total}
	cores := f.coresLocked()
	found := false
	for _, c := range cores {
		if !c.Bundled && c.Version == version {
			found = true
		}
	}
	if !found {
		f.cores = append(cores, model.InstalledCore{Version: version, Channel: channel, Path: "/Users/me/Library/Application Support/Lanflow/cores/" + version + "/mihomo", Size: total, InstalledAt: time.Now(), SHA256: "ab12…", Source: "https://github.com/MetaCubeX/mihomo/releases/download/" + version})
	}
	f.mu.Unlock()
	f.emit(app.TopicCoreDownload, app.TopicCores)
	return nil
}

// UseCore switches after a short "启动" phase.
func (f *Fake) UseCore(version string) error {
	f.mu.Lock()
	if f.coreDL.Active {
		f.mu.Unlock()
		return errors.New(i18n.T("内核正在下载中，请稍候。"))
	}
	installed := version == ""
	for _, c := range f.coresLocked() {
		if !c.Bundled && c.Version == version {
			installed = true
		}
	}
	if !installed {
		f.mu.Unlock()
		return fmt.Errorf(i18n.T("内核 %s 未安装。"), version)
	}
	label := version
	if label == "" {
		label = i18n.T("内置")
	}
	f.coreDL = model.CoreDownload{Active: true, Phase: "启动", Version: label}
	f.mu.Unlock()
	f.emit(app.TopicCoreDownload)
	f.wait()
	f.mu.Lock()
	f.set.CoreVersion = version
	f.set.CorePath = ""
	if version != "" {
		f.set.CorePath = "/Users/me/Library/Application Support/Lanflow/cores/" + version + "/mihomo"
	}
	if f.st.Core == model.CoreRunning || version != "" {
		f.st.CoreVersion = version
		if version == "" {
			f.st.CoreVersion = "v1.19.33"
		}
	}
	f.coresLocked()
	f.coreDL = model.CoreDownload{Done: label}
	f.mu.Unlock()
	f.emit(app.TopicCoreDownload, app.TopicCores, app.TopicSettings, app.TopicStatus)
	return nil
}

func (f *Fake) RemoveCore(version string) error {
	return f.do(func() error {
		if version == "" {
			return errors.New(i18n.T("内置内核随应用安装，不能删除。"))
		}
		if version == f.set.CoreVersion {
			return errors.New(i18n.T("这是正在使用的内核，请先切换到其他版本再删除。"))
		}
		cores := f.coresLocked()
		for i, c := range cores {
			if !c.Bundled && c.Version == version {
				f.cores = append(cores[:i], cores[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf(i18n.T("内核 %s 未安装。"), version)
	}, app.TopicCores)
}

func (f *Fake) CheckCoreUpdate() (model.CoreUpdateInfo, error) {
	f.wait()
	f.mu.Lock()
	channel := f.set.CoreChannel
	if channel == "" {
		channel = model.CoreChannelStable
	}
	current := f.st.CoreVersion
	f.mu.Unlock()
	list := fakeReleases[channel]
	info := model.CoreUpdateInfo{Channel: channel, Current: current, CheckedAt: time.Now()}
	if len(list) > 0 {
		info.Latest, info.Notes = list[0].Version, list[0].Notes
		info.Available = info.Latest != current
	}
	return info, nil
}

// CoreDownload returns the simulated download progress.
func (f *Fake) CoreDownload() model.CoreDownload { f.mu.Lock(); defer f.mu.Unlock(); return f.coreDL }

// DownloadCore simulates a download in a few steps, then a running core.
func (f *Fake) DownloadCore(version string) error {
	if version == "" {
		version = "v1.19.33"
	}
	const total = int64(18 << 20)
	f.mu.Lock()
	if f.coreDL.Active {
		f.mu.Unlock()
		return errors.New(i18n.T("内核正在下载中，请稍候。"))
	}
	f.coreDL = model.CoreDownload{Active: true, Phase: "下载", Version: version, Total: total}
	f.mu.Unlock()
	f.emit(app.TopicCoreDownload)
	for i := 1; i <= 8; i++ {
		f.wait()
		f.mu.Lock()
		f.coreDL.Bytes = total * int64(i) / 8
		f.mu.Unlock()
		f.emit(app.TopicCoreDownload)
	}
	for _, ph := range []string{"校验", "安装", "启动"} {
		f.mu.Lock()
		f.coreDL.Phase = ph
		f.mu.Unlock()
		f.emit(app.TopicCoreDownload)
		f.wait()
	}
	f.mu.Lock()
	f.coreDL = model.CoreDownload{Done: version}
	f.st.Core, f.st.CoreError, f.st.CoreVersion = model.CoreRunning, "", version
	f.mu.Unlock()
	f.emit(app.TopicCoreDownload, app.TopicStatus)
	return nil
}

// ExportSettings writes the fake's settings, overrides and rules to path.
func (f *Fake) ExportSettings(path string, includeSubs bool) error {
	f.wait()
	f.mu.Lock()
	set, ov, rules, subs := f.set, f.overridesLocked(), append([]model.CustomRule(nil), f.custom...), append([]model.Subscription(nil), f.subs...)
	f.mu.Unlock()
	set.CorePath = ""
	ex := model.SettingsExport{App: "lanflow", Format: 1, ExportedAt: time.Now(), Settings: &set, Overrides: &ov, CustomRules: rules}
	if includeSubs {
		for _, s := range subs {
			if s.Source == "url" && s.URL != "" {
				ex.Subscriptions = append(ex.Subscriptions, model.ExportedSubscription{Name: s.Name, URL: s.URL, UserAgent: s.UserAgent, ViaProxy: s.ViaProxy, AutoUpdateHours: s.AutoUpdateHours})
			}
		}
	}
	data, err := json.MarshalIndent(ex, "", "  ")
	if err != nil {
		return fmt.Errorf(i18n.T("导出设置失败：%v"), err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf(i18n.T("写入文件失败：%v"), err)
	}
	return nil
}

// ImportSettings reads a SettingsExport and applies its settings.
func (f *Fake) ImportSettings(path string) (string, error) {
	f.wait()
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf(i18n.T("读取文件失败：%v"), err)
	}
	var ex model.SettingsExport
	if err := json.Unmarshal(data, &ex); err != nil || ex.App != "lanflow" {
		return "", errors.New(i18n.T("这不是澜流导出的设置文件。"))
	}
	var parts []string
	f.mu.Lock()
	if ex.Settings != nil {
		ex.Settings.CorePath, ex.Settings.TUN = f.set.CorePath, f.set.TUN
		f.set = *ex.Settings
		i18n.SetLang(f.set.Language)
		parts = append(parts, i18n.T("设置"))
	}
	if ex.Overrides != nil {
		if len(ex.Overrides.List) > 0 {
			f.ovl = append([]model.Override(nil), ex.Overrides.List...)
		} else {
			f.ovl = nil
			if ex.Overrides.Global != "" {
				f.ovl = append(f.ovl, model.Override{ID: "global", Name: i18n.T("全局覆写"), Enabled: true, Global: true, Source: model.OverrideLocal, Text: ex.Overrides.Global})
			}
			for id, text := range ex.Overrides.PerSub {
				f.ovl = append(f.ovl, model.Override{ID: id, Name: id, Enabled: true, Source: model.OverrideLocal, Text: text})
			}
		}
		parts = append(parts, i18n.T("覆写"))
	}
	if len(ex.CustomRules) > 0 {
		f.custom = ex.CustomRules
		parts = append(parts, fmt.Sprintf(i18n.T("%d 条自定义规则"), len(ex.CustomRules)))
	}
	f.mu.Unlock()
	f.emit(app.TopicSettings, app.TopicRules, app.TopicStatus)
	if len(parts) == 0 {
		return "", errors.New(i18n.T("文件中没有可导入的内容。"))
	}
	return i18n.T("已导入：") + strings.Join(parts, i18n.T("、")), nil
}
func (f *Fake) Shutdown() {
	select {
	case <-f.stop:
	default:
		close(f.stop)
	}
}

// ATrustReference is the real template.
func (f *Fake) ATrustReference() string { return app.ATrustReferenceYAML() }

// ImportATrustConfig reads the file for real; nothing is saved.
func (f *Fake) ImportATrustConfig(path string) (model.ATrustConfig, string, error) {
	f.wait()
	return app.ReadATrustConfig(path)
}

// ExportATrustConfig writes the current lists for real.
func (f *Fake) ExportATrustConfig(path string) error {
	f.wait()
	return app.WriteATrustConfig(path, f.Settings().ATrust)
}

// ---- 流量统计 ----

// TrafficStats fabricates a day/week/month/all of statistics from a fixed
// seed, scaled by the range, so the 统计 page has something to show.
func (f *Fake) TrafficStats(rangeKey string) model.TrafficStats {
	f.mu.Lock()
	reset := f.statsReset
	f.mu.Unlock()
	now := time.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	st := model.TrafficStats{Range: rangeKey, Buckets: map[model.StatsDimension][]model.StatsBucket{}, Days: 23}
	scale, days := 1.0, 1
	switch rangeKey {
	case model.StatsWeek:
		scale, days = 5.2, int(day.Weekday()+6)%7+1
		st.Since = day.AddDate(0, 0, 1-days)
	case model.StatsMonth:
		scale, days = 14, day.Day()
		st.Since = time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, day.Location())
	case model.StatsAll:
		scale, days = 21, 23
		st.Since = day.AddDate(0, 0, -22)
	default:
		rangeKey, st.Range = model.StatsToday, model.StatsToday
		st.Since = day
	}
	if reset {
		return st
	}
	r := rand.New(rand.NewSource(42))
	bucket := func(key, detail string, req int64, mb float64) model.StatsBucket {
		down := int64(mb * scale * float64(1<<20) * (0.7 + 0.6*r.Float64()))
		return model.StatsBucket{Key: key, Detail: detail, Requests: int64(float64(req)*scale) + 1, Upload: down / int64(6+r.Intn(20)), Download: down,
			LastSeen: now.Add(-time.Duration(r.Intn(6*3600)) * time.Second)}
	}
	add := func(dim model.StatsDimension, bs ...model.StatsBucket) {
		sort.Slice(bs, func(i, j int) bool { return bs[i].Total() > bs[j].Total() })
		st.Buckets[dim] = bs
		if dim == model.StatsDevice {
			for _, b := range bs {
				st.Totals.Upload += b.Upload
				st.Totals.Download += b.Download
				st.Totals.Requests += b.Requests
			}
			st.Totals.Devices = len(bs)
		}
	}
	add(model.StatsDevice, bucket("127.0.0.1", "", 1840, 1900), bucket("192.168.1.8", "", 310, 620), bucket("192.168.1.15", "", 96, 140))
	add(model.StatsDomain, bucket("googlevideo.com", "", 420, 1200), bucket("github.com", "", 180, 310), bucket("apple.com", "", 260, 240),
		bucket("openai.com", "", 150, 95), bucket("slack.com", "", 210, 88), bucket("bilibili.com", "", 190, 330), bucket("spotify.com", "", 64, 160),
		bucket("notion.so", "", 120, 42), bucket("discord.com", "", 88, 36), bucket("telegram.org", "", 140, 70), bucket("google.com", "", 400, 120))
	add(model.StatsHost, bucket("rr3---sn-i3b7kn7s.googlevideo.com", "googlevideo.com", 220, 800), bucket("i.ytimg.com", "youtube.com", 180, 120),
		bucket("api.github.com", "github.com", 160, 260), bucket("www.apple.com", "apple.com", 200, 180), bucket("api.openai.com", "openai.com", 150, 95),
		bucket("slack.com", "slack.com", 210, 88), bucket("www.bilibili.com", "bilibili.com", 190, 330), bucket("www.google.com", "google.com", 400, 120))
	add(model.StatsProcess, bucket("Google Chrome", "/Applications/Google Chrome.app", 760, 1500), bucket("Safari", "/Applications/Safari.app", 640, 620),
		bucket("git", "/usr/bin/git", 120, 260), bucket("Slack", "/Applications/Slack.app", 210, 88), bucket("Python", "/usr/local/bin/python3", 150, 95),
		bucket("Spotify", "/Applications/Spotify.app", 64, 160), bucket("Telegram", "/Applications/Telegram.app", 140, 70), bucket("", "", 60, 12))
	add(model.StatsRule, bucket("GeoSite,youtube", "youtube", 400, 1320), bucket("DomainSuffix,google.com", "google.com", 400, 120), bucket("GeoSite,cn", "cn", 190, 330),
		bucket("GeoSite,github", "github", 180, 310), bucket("GeoIP,CN", "CN", 260, 240), bucket("DomainSuffix,openai.com", "openai.com", 150, 95),
		bucket("IPCIDR,149.154.160.0/20", "149.154.160.0/20", 140, 70), bucket("Match,", "", 90, 30))
	add(model.StatsProxy, bucket("节点选择/东京 03", "东京 03", 1100, 1000), bucket("流媒体/香港 01", "香港 01", 480, 1480), bucket("DIRECT", "DIRECT", 620, 600))
	add(model.StatsCountry, bucket("US", "", 1200, 2000), bucket("CN", "", 520, 600), bucket("JP", "", 180, 140), bucket("NL", "", 140, 70), bucket("SG", "", 60, 30), bucket("", "", 90, 20))
	add(model.StatsInbound, bucket("HTTP", "", 1600, 2300), bucket("Socks5", "", 420, 300), bucket("Tun", "", 220, 60))
	if rangeKey == model.StatsToday {
		st.Hourly = true
		st.Series = make([]model.StatsPoint, 24)
		h := now.Hour()
		for i := range st.Series {
			st.Series[i].Time = day.Add(time.Duration(i) * time.Hour)
			if i <= h {
				w := 0.2 + 0.8*float64((i+6)%24)/24
				st.Series[i].Download = int64(w * float64(st.Totals.Download) / float64(h+1) * (0.6 + 0.8*r.Float64()))
				st.Series[i].Upload = st.Series[i].Download / 9
				st.Series[i].Requests = int64(w * float64(st.Totals.Requests) / float64(h+1))
			}
		}
	} else {
		st.Series = make([]model.StatsPoint, 30)
		for i := range st.Series {
			d := day.AddDate(0, 0, i-29)
			st.Series[i].Time = d
			if d.Before(st.Since) {
				continue
			}
			st.Series[i].Download = int64(float64(st.Totals.Download) / float64(days) * (0.4 + 1.2*r.Float64()))
			st.Series[i].Upload = st.Series[i].Download / 9
			st.Series[i].Requests = int64(float64(st.Totals.Requests) / float64(days))
		}
	}
	return st
}

// ResetTrafficStats empties the statistics (until the fake is rebuilt).
func (f *Fake) ResetTrafficStats() error {
	return f.do(func() error { f.statsReset = true; return nil }, app.TopicStats)
}

// ClearSubscriptions puts the fake in the first-run state (no
// subscription, core stopped) so UI tests and snapshots can show the empty
// states of docs/design.md §8.1.
func (f *Fake) ClearSubscriptions() {
	f.mu.Lock()
	f.subs = nil
	f.st.HasSubscriptions = false
	f.st.CurrentSubscription = ""
	f.st.Core = model.CoreStopped
	f.mu.Unlock()
	f.emit(app.TopicSubs)
	f.emit(app.TopicStatus)
}
