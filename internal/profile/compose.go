package profile

import (
	"fmt"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"

	"lanflow/internal/model"
)

// Layer names shown in the runtime config view.
const (
	layerSub         = "订阅"
	layerSubOverride = "订阅覆写"
	layerGlobal      = "全局覆写"
	layerCustom      = "自定义规则"
	layerLanflow     = "澜流设置"
)

// Probe listener / group names (docs/exit-map.md §3.2).
const (
	probeListenerName = "lanflow-probe"
	probeGroupName    = "LANFLOW-PROBE"
)

// Keys removed from the final config: the controller is passed on the
// command line and only mixed-port is used for inbound traffic.
var strippedKeys = []string{
	"external-controller", "external-controller-tls", "external-controller-unix",
	"external-controller-pipe", "external-ui", "external-ui-url", "external-ui-name",
	"secret", "port", "socks-port", "redir-port", "tproxy-port",
}

// Built-in policies accepted as custom rule targets.
var builtinTargets = []string{"DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE"}

var defaultNameservers = []string{"https://doh.pub/dns-query", "https://dns.alidns.com/dns-query"}

// IPv6 handling. Mihomo's own `ipv6` switch does more than disable AAAA:
// when it is off the core strips the TUN's inet6 address (config.parseIPV6),
// so the TUN routes only IPv4 and every IPv6 connection of every app leaves
// the machine directly, unproxied, wherever the LAN offers IPv6 (even a ULA
// with NAT66). The core switch therefore always stays on and the TUN always
// gets an inet6 address (auto-route then takes ::/0 as well); Lanflow's own
// IPv6 setting governs the resolver (dns.ipv6: no AAAA when off) and, when
// off, rejects IPv6 destinations at the top of the rules, so an IPv6
// literal entering the TUN is refused (RST, apps fall back to IPv4) instead
// of being dialed. In global mode rules are skipped and such a connection
// goes to the proxy, which is still not a leak from this machine.
const (
	TUNInet6Address = "fdfe:dcba:9876::1/126" // Mihomo's default, made explicit
	ipv6RejectRule  = "IP-CIDR6,::/0,REJECT,no-resolve"
)

// composer carries the document being built and the layer attribution.
type composer struct {
	root    *yaml.Node
	layer   string
	keySrc  map[string]string     // top-level key -> layer that last set it
	nodeSrc map[*yaml.Node]string // rule item nodes -> layer
}

func (c *composer) touched(key string, replaced bool) {
	if replaced || c.keySrc[key] == "" {
		c.keySrc[key] = c.layer
	}
}

// set replaces a top-level key with v on behalf of the current layer.
func (c *composer) set(key string, v *yaml.Node) {
	mapSet(c.root, key, v)
	c.keySrc[key] = c.layer
}

// sub returns the top-level mapping key, creating it (as a mapping) when it
// is absent or not a mapping.
func (c *composer) sub(key string) *yaml.Node {
	v := deref(mapGet(c.root, key))
	if v == nil || v.Kind != yaml.MappingNode {
		v = mapNode()
	}
	c.set(key, v)
	return v
}

// seq returns the top-level sequence key, creating it when absent or not a
// sequence. Items appended later are attributed individually (see render),
// so an existing list keeps its key attribution.
func (c *composer) seq(key string) *yaml.Node {
	v := deref(mapGet(c.root, key))
	if v == nil || v.Kind != yaml.SequenceNode {
		v = seqNode()
		c.set(key, v)
		return v
	}
	if mapIndex(c.root, key) < 0 {
		mapSet(c.root, key, v)
	}
	return v
}

// mark attributes n and its descendants to the current layer.
func (c *composer) mark(n *yaml.Node) *yaml.Node {
	walk(n, func(x *yaml.Node) { c.nodeSrc[x] = c.layer })
	return n
}

func (c *composer) applyOverrideLayer(text, layer string) error {
	over, err := parseOverride(text)
	if err != nil {
		return fmt.Errorf("%s%s", layer, err.Error())
	}
	if over == nil {
		return nil
	}
	c.layer = layer
	walk(over, func(n *yaml.Node) { c.nodeSrc[n] = layer })
	if err := mergeOverride(c.root, over, c); err != nil {
		return fmt.Errorf("%s%s", layer, err.Error())
	}
	return nil
}

func compose(in Input) (Output, error) {
	root, err := parseDoc(in.Subscription)
	if err != nil {
		return Output{}, fmt.Errorf("订阅内容%s", err.Error())
	}
	c := &composer{root: root, layer: layerSub, keySrc: map[string]string{}, nodeSrc: map[*yaml.Node]string{}}
	for i := 0; i+1 < len(root.Content); i += 2 {
		c.keySrc[root.Content[i].Value] = layerSub
	}

	if err := c.applyOverrideLayer(in.SubOverride, layerSubOverride); err != nil {
		return Output{}, err
	}
	if err := c.applyOverrideLayer(in.GlobalOverride, layerGlobal); err != nil {
		return Output{}, err
	}
	for _, l := range in.Overrides {
		if err := c.applyOverrideLayer(l.Text, l.Name); err != nil {
			return Output{}, err
		}
	}

	// ---- Lanflow settings ----
	c.layer = layerLanflow
	if in.DNS.TakeOver {
		c.set("dns", dnsNode(in.DNS, in.Managed.IPv6))
		if len(in.DNS.Hosts) > 0 {
			c.set("hosts", kvMap(in.DNS.Hosts))
		}
	} else if d := deref(mapGet(root, "dns")); in.Managed.TUN && (d == nil || d.Kind != yaml.MappingNode || len(d.Content) == 0) {
		c.set("dns", dnsNode(model.DNSSettings{}, in.Managed.IPv6))
	}

	if in.Sniffer.TakeOver {
		c.set("sniffer", snifferNode(in.Sniffer))
	} else {
		sn := c.sub("sniffer")
		mapSet(sn, "enable", boolNode(in.Sniffer.Enable))
	}

	applyGeo(c, in.Geo)

	if !in.Managed.IPv6 {
		// IPv6 off: no AAAA from whichever dns section is in force, and
		// IPv6 destinations refused ahead of the subscription's rules (see
		// TUNInet6Address). The custom rules, added next, stay in front of
		// it: a user's own IP-CIDR6 rule is a deliberate exception.
		if d := deref(mapGet(root, "dns")); d != nil && d.Kind == yaml.MappingNode {
			mapSet(d, "ipv6", boolNode(false))
		}
		rs := c.seq("rules")
		rs.Content = append([]*yaml.Node{c.mark(strNode(ipv6RejectRule))}, rs.Content...)
	}

	c.layer = layerCustom
	if len(in.CustomRules) > 0 {
		if err := c.addCustomRules(in.CustomRules); err != nil {
			return Output{}, err
		}
	}

	// ---- Managed fields ----
	c.layer = layerLanflow
	m := in.Managed
	port := m.MixedPort
	if port <= 0 {
		port = 7890
	}
	c.set("mixed-port", intNode(port))
	c.set("allow-lan", boolNode(m.AllowLAN))
	if m.AllowLAN {
		c.set("bind-address", strNode("*"))
	}
	mode := string(m.Mode)
	if mode == "" {
		mode = string(model.ModeRule)
	}
	c.set("mode", strNode(mode))
	level := m.LogLevel
	if level == "" {
		level = "info"
	}
	c.set("log-level", strNode(level))
	c.set("ipv6", boolNode(true)) // always: see TUNInet6Address

	prof := c.sub("profile")
	mapSet(prof, "store-selected", boolNode(true))

	tun := c.sub("tun")
	stack := m.TUNStack
	if stack == "" {
		stack = "mixed"
	}
	mapSet(tun, "enable", boolNode(m.TUN))
	mapSet(tun, "stack", strNode(stack))
	mapSet(tun, "auto-route", boolNode(true))
	mapSet(tun, "inet6-address", strSeq([]string{TUNInet6Address}))
	mapSet(tun, "auto-detect-interface", boolNode(true))
	mapSet(tun, "dns-hijack", strSeq([]string{"any:53", "tcp://any:53"}))
	mapSet(tun, "strict-route", boolNode(false))
	if runtime.GOOS == "darwin" {
		mapSet(tun, "device", strNode(TUNDevice)) // a fixed name tells our utun from aTrust's
	}

	// Probe listener and group are always removed first so that a stale
	// copy in an override cannot linger.
	removeNamed(deref(mapGet(root, "listeners")), probeListenerName)
	removeNamed(deref(mapGet(root, "proxy-groups")), probeGroupName)
	if m.ProbePort > 0 {
		ls := c.seq("listeners")
		ls.Content = append(ls.Content, c.mark(mapNode(
			"name", strNode(probeListenerName),
			"type", strNode("http"),
			"listen", strNode("127.0.0.1"),
			"port", intNode(m.ProbePort),
			"proxy", strNode(probeGroupName),
		)))
		gs := c.seq("proxy-groups")
		gs.Content = append(gs.Content, c.mark(mapNode(
			"name", strNode(probeGroupName),
			"type", strNode("select"),
			"proxies", strSeq([]string{"DIRECT"}),
			"include-all", boolNode(true),
			"hidden", boolNode(true),
		)))
	}

	applyCapture(c, in.Capture)
	applyAdvanced(c, in.Advanced)
	applyCoexist(c, in.Coexist, m.TUN)
	warns := scrubCaptureRefs(c)
	c.layer = layerLanflow

	for _, k := range strippedKeys {
		mapDelete(root, k)
		delete(c.keySrc, k)
	}
	applyAdvancedPorts(c, in.Advanced, port)
	resolveDanglingAliases(root)

	out := Output{Groups: groupNames(root, true), Warnings: warns}
	out.YAML, out.Lines, err = c.render()
	if err != nil {
		return Output{}, err
	}
	return out, nil
}

// ---- Lanflow sections ----

func kvMap(kv []model.KV) *yaml.Node {
	n := mapNode()
	for _, p := range kv {
		mapSet(n, p.Key, strNode(p.Value))
	}
	return n
}

func dnsNode(d model.DNSSettings, ipv6 bool) *yaml.Node {
	mode := d.EnhancedMode
	if mode == "" {
		mode = "fake-ip"
	}
	ns := d.Nameserver
	if len(ns) == 0 {
		ns = defaultNameservers
	}
	n := mapNode(
		"enable", boolNode(true),
		"ipv6", boolNode(ipv6),
		"enhanced-mode", strNode(mode),
	)
	if mode == "fake-ip" {
		mapSet(n, "fake-ip-range", strNode("198.18.0.1/16"))
		if len(d.FakeIPFilter) > 0 {
			mapSet(n, "fake-ip-filter", strSeq(d.FakeIPFilter))
		}
	}
	mapSet(n, "nameserver", strSeq(ns))
	if len(d.ProxyServerNameserver) > 0 {
		mapSet(n, "proxy-server-nameserver", strSeq(d.ProxyServerNameserver))
	}
	if len(d.DirectNameserver) > 0 {
		mapSet(n, "direct-nameserver", strSeq(d.DirectNameserver))
	}
	if len(d.Policy) > 0 {
		mapSet(n, "nameserver-policy", kvMap(d.Policy))
	}
	if d.RespectRules {
		mapSet(n, "respect-rules", boolNode(true))
	}
	mapSet(n, "use-system-hosts", boolNode(d.UseSystemHosts))
	if len(d.Hosts) > 0 {
		mapSet(n, "use-hosts", boolNode(true))
	}
	return n
}

// splitPorts turns "80, 8080-8880" into ["80", "8080-8880"].
func splitPorts(s, def string) []string {
	if strings.TrimSpace(s) == "" {
		s = def
	}
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func snifferNode(s model.SnifferSettings) *yaml.Node {
	n := mapNode(
		"enable", boolNode(s.Enable),
		"override-destination", boolNode(s.OverrideDestination),
		"parse-pure-ip", boolNode(s.ParsePureIP),
	)
	sniff := mapNode()
	if s.HTTP {
		mapSet(sniff, "HTTP", mapNode("ports", strSeq(splitPorts(s.HTTPPorts, "80, 8080-8880")), "override-destination", boolNode(s.OverrideDestination)))
	}
	if s.TLS {
		mapSet(sniff, "TLS", mapNode("ports", strSeq(splitPorts(s.TLSPorts, "443, 8443"))))
	}
	if s.QUIC {
		mapSet(sniff, "QUIC", mapNode("ports", strSeq(splitPorts(s.QUICPorts, "443, 8443"))))
	}
	mapSet(n, "sniff", sniff)
	if len(s.ForceDomain) > 0 {
		mapSet(n, "force-domain", strSeq(s.ForceDomain))
	}
	if len(s.SkipDomain) > 0 {
		mapSet(n, "skip-domain", strSeq(s.SkipDomain))
	}
	if len(s.SkipDstAddress) > 0 {
		mapSet(n, "skip-dst-address", strSeq(s.SkipDstAddress))
	}
	return n
}

func applyGeo(c *composer, g model.GeoSettings) {
	if g.GeoIP == "" && g.GeoSite == "" && g.MMDB == "" && g.ASN == "" && g.Mode == "" && !g.AutoUpdate && g.IntervalHours == 0 {
		return
	}
	if g.GeoIP != "" || g.GeoSite != "" || g.MMDB != "" || g.ASN != "" {
		gx := c.sub("geox-url")
		for k, v := range map[string]string{"geoip": g.GeoIP, "geosite": g.GeoSite, "mmdb": g.MMDB, "asn": g.ASN} {
			if v != "" {
				mapSet(gx, k, strNode(v))
			}
		}
		// Deterministic key order for the four URLs.
		sortMapping(gx, []string{"geoip", "geosite", "mmdb", "asn"})
	}
	if g.Mode != "" {
		c.set("geodata-mode", boolNode(g.Mode == "dat"))
	}
	c.set("geo-auto-update", boolNode(g.AutoUpdate))
	if g.IntervalHours > 0 {
		c.set("geo-update-interval", intNode(g.IntervalHours))
	}
}

// sortMapping reorders the keys of m listed in order to the front, in that
// order, keeping everything else after them in their original order.
func sortMapping(m *yaml.Node, order []string) {
	var front, rest []*yaml.Node
	seen := map[string]bool{}
	for _, k := range order {
		if i := mapIndex(m, k); i >= 0 {
			front = append(front, m.Content[i], m.Content[i+1])
			seen[k] = true
		}
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if !seen[m.Content[i].Value] {
			rest = append(rest, m.Content[i], m.Content[i+1])
		}
	}
	m.Content = append(front, rest...)
}

// ---- custom rules ----

// namesOf returns the `name` of each mapping item in a sequence.
func namesOf(seq *yaml.Node) []string {
	seq = deref(seq)
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	var out []string
	for _, it := range seq.Content {
		if n := deref(mapGet(deref(it), "name")); n != nil && n.Kind == yaml.ScalarNode {
			out = append(out, n.Value)
		}
	}
	return out
}

// groupNames returns proxy-group names in order, optionally hiding the probe group.
func groupNames(root *yaml.Node, hideProbe bool) []string {
	names := namesOf(mapGet(root, "proxy-groups"))
	if !hideProbe {
		return names
	}
	out := names[:0:0]
	for _, n := range names {
		if n != probeGroupName {
			out = append(out, n)
		}
	}
	return out
}

func (c *composer) addCustomRules(rules []model.CustomRule) error {
	targets := map[string]bool{}
	for _, t := range builtinTargets {
		targets[t] = true
	}
	for _, n := range groupNames(c.root, true) {
		targets[n] = true
	}
	for _, n := range namesOf(mapGet(c.root, "proxies")) {
		targets[n] = true
	}
	groups := groupNames(c.root, true)

	items := make([]*yaml.Node, 0, len(rules))
	for i, r := range rules {
		typ := strings.ToUpper(strings.TrimSpace(r.Type))
		payload := strings.TrimSpace(r.Payload)
		target := strings.TrimSpace(r.Target)
		if typ == "" || payload == "" {
			return fmt.Errorf("自定义规则第 %d 条缺少类型或内容，请补全后再试", i+1)
		}
		if target == "" {
			return fmt.Errorf("自定义规则第 %d 条「%s,%s」没有目标，请选择一个代理组、DIRECT 或 REJECT", i+1, typ, payload)
		}
		if !targets[target] {
			hint := "请选择一个代理组、DIRECT 或 REJECT"
			if len(groups) > 0 {
				hint += "（当前订阅的代理组：" + strings.Join(groups, "、") + "）"
			}
			return fmt.Errorf("自定义规则第 %d 条「%s,%s」的目标「%s」不存在：%s", i+1, typ, payload, target, hint)
		}
		items = append(items, c.mark(strNode(typ+","+payload+","+target)))
	}
	rs := c.seq("rules")
	rs.Content = append(items, rs.Content...)
	return nil
}

// removeNamed deletes mapping items whose `name` equals name from seq.
func removeNamed(seq *yaml.Node, name string) {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return
	}
	out := seq.Content[:0]
	for _, it := range seq.Content {
		if n := deref(mapGet(deref(it), "name")); n != nil && n.Value == name {
			continue
		}
		out = append(out, it)
	}
	seq.Content = out
}

// resolveDanglingAliases replaces alias nodes whose anchor is no longer in
// the tree (because an override replaced it) by a copy of the target.
func resolveDanglingAliases(root *yaml.Node) {
	anchors := map[*yaml.Node]bool{}
	walk(root, func(n *yaml.Node) {
		if n.Anchor != "" {
			anchors[n] = true
		}
	})
	var fix func(n *yaml.Node)
	fix = func(n *yaml.Node) {
		for i, ch := range n.Content {
			if ch.Kind == yaml.AliasNode && ch.Alias != nil && !anchors[ch.Alias] {
				cp := cloneNode(ch.Alias)
				cp.Anchor = ""
				n.Content[i] = cp
				fix(cp)
				continue
			}
			fix(ch)
		}
	}
	fix(root)
}

func cloneNode(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	cp := *n
	cp.Content = make([]*yaml.Node, len(n.Content))
	for i, ch := range n.Content {
		cp.Content[i] = cloneNode(ch)
	}
	return &cp
}

// ---- rendering ----

// render encodes the document key by key so each top-level key (and each
// rule) can be attributed to its layer.
func (c *composer) render() ([]byte, []model.RuntimeLine, error) {
	root := c.root
	if len(root.Content) == 0 {
		return []byte("{}\n"), []model.RuntimeLine{{Text: "{}", Source: layerLanflow}}, nil
	}
	var sb strings.Builder
	var lines []model.RuntimeLine
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i], root.Content[i+1]
		single := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{k, v}}
		text, err := encodeDoc(single)
		if err != nil {
			return nil, nil, err
		}
		sb.Write(text)
		src := c.keySrc[k.Value]
		if src == "" {
			src = layerSub
		}
		ls := strings.Split(strings.TrimRight(string(text), "\n"), "\n")
		if deref(v).Kind == yaml.SequenceNode {
			lines = append(lines, c.seqLines(ls, deref(v), src)...)
			continue
		}
		for j, l := range ls {
			rl := model.RuntimeLine{Text: l}
			if j == 0 {
				rl.Source = src
			}
			lines = append(lines, rl)
		}
	}
	return []byte(sb.String()), lines, nil
}

// seqLines attributes each item of a top-level sequence (rules, proxies,
// proxy-groups, listeners...). ls is the encoded text of the single-key
// document, whose first line is the key.
func (c *composer) seqLines(ls []string, seq *yaml.Node, keySrc string) []model.RuntimeLine {
	out := make([]model.RuntimeLine, 0, len(ls))
	out = append(out, model.RuntimeLine{Text: ls[0], Source: keySrc})
	if len(ls) < 2 {
		return out
	}
	prefix := ls[1][:len(ls[1])-len(strings.TrimLeft(ls[1], " "))] + "- "
	starts := 0
	for _, l := range ls[1:] {
		if strings.HasPrefix(l, prefix) {
			starts++
		}
	}
	if starts != len(seq.Content) {
		// Unexpected layout (e.g. flow style); fall back to per-key attribution.
		for _, l := range ls[1:] {
			out = append(out, model.RuntimeLine{Text: l})
		}
		return out
	}
	idx := -1
	for _, l := range ls[1:] {
		rl := model.RuntimeLine{Text: l}
		if strings.HasPrefix(l, prefix) {
			idx++
			src := c.nodeSrc[seq.Content[idx]]
			if src == "" {
				src = layerSub
			}
			rl.Source = src
		}
		out = append(out, rl)
	}
	return out
}
