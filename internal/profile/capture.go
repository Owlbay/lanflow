package profile

import (
	"fmt"
	"net/netip"
	"strings"

	"gopkg.in/yaml.v3"
)

// HTTPS 选择性解密 layer (docs/capture.md §3). Selected connections go to
// a `type: http` outbound with a random name (LANFLOW-MITM-<r>) that leads
// to the capture agent; the agent's upstream connections come back in
// through the password-protected listener lanflow-mitm-up and continue
// with the remaining rules. Nothing outside this layer may refer to the
// outbound: scrubCaptureRefs removes such references after composition.

const (
	layerCapture = "抓包"

	// CaptureOutboundPrefix starts the name of every decrypting outbound.
	CaptureOutboundPrefix = "LANFLOW-MITM"
	// CaptureUpListener is the return listener's name.
	CaptureUpListener = "lanflow-mitm-up"
	captureSelSet     = "lanflow-mitm-sel"
	captureSelLANSet  = "lanflow-mitm-sel-lan"
	captureBypassSet  = "lanflow-mitm-bypass"
	capturePrivSet    = "lanflow-mitm-private"
	captureAppSet     = "lanflow-mitm-app-"
	captureNamePrefix = "lanflow-mitm-"
)

// Capture is the decryption input; the zero value adds nothing (the
// scrubbing of references runs regardless).
type Capture struct {
	AgentPort int
	UpPort    int
	UpUser    string
	UpPass    string
	// Domain selection: patterns as in model.CaptureSettings.Domains,
	// carried by Domain (user/pass/outbound).
	Domains []string
	Domain  CaptureSelector
	// Apps are the program selections (M2), each with its own outbound.
	Apps   []CaptureApp
	Bypass []string // effective bypass list (built-in minus disabled, plus the user's), private entries excluded
	// Private are the built-in private-network entries in force; they skip
	// every selection except the domain patterns in AllowPrivate.
	Private      []string
	AllowPrivate []string
	Ports        string // "80/443/8443"
	IPv6         bool   // Lanflow's IPv6 setting: off excludes IPv6 literals
}

// CaptureSelector is one decrypting outbound and its credentials.
type CaptureSelector struct {
	Outbound string // LANFLOW-MITM-<r>[-A<i>]
	User     string
	Pass     string
}

// CaptureApp is a program selection: Rules are classical lines such as
// "PROCESS-PATH-WILDCARD,/Applications/Foo.app/*".
type CaptureApp struct {
	CaptureSelector
	Rules []string
}

func (c Capture) active() bool {
	return c.AgentPort > 0 && c.UpPort > 0 && (len(c.Domains) > 0 && c.Domain.Outbound != "" || len(c.Apps) > 0)
}

// CaptureDomainRule turns a selection pattern into a classical rule line.
func CaptureDomainRule(p string) string {
	p = strings.TrimSpace(p)
	if pf, err := netip.ParsePrefix(p); err == nil {
		if pf.Addr().Is4() {
			return "IP-CIDR," + pf.Masked().String()
		}
		return "IP-CIDR6," + pf.Masked().String()
	}
	switch {
	case strings.HasPrefix(p, "+."):
		return "DOMAIN-SUFFIX," + p[2:]
	case strings.ContainsAny(p, "*?"):
		return "DOMAIN-WILDCARD," + p
	default:
		return "DOMAIN," + p
	}
}

func captureRuleLines(patterns []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range patterns {
		if strings.TrimSpace(p) == "" {
			continue
		}
		l := CaptureDomainRule(p)
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

func inlineProvider(payload []string) *yaml.Node {
	return mapNode("type", strNode("inline"), "behavior", strNode("classical"), "payload", strSeq(payload))
}

// applyCapture writes the layer: outbounds, return listener, inline rule
// providers and the rules in front of the custom rules. Same-named items
// are removed first so an override cannot keep a stale copy.
func applyCapture(c *composer, cp Capture) {
	c.layer = layerCapture
	if !cp.active() {
		return
	}
	ports := cp.Ports
	if strings.TrimSpace(ports) == "" {
		ports = "80/443/8443"
	}
	sels := []CaptureSelector{}
	if len(cp.Domains) > 0 && cp.Domain.Outbound != "" {
		sels = append(sels, cp.Domain)
	}
	for _, a := range cp.Apps {
		sels = append(sels, a.CaptureSelector)
	}

	ps := c.seq("proxies")
	for _, s := range sels {
		removeNamed(ps, s.Outbound)
		ps.Content = append(ps.Content, c.mark(mapNode(
			"name", strNode(s.Outbound),
			"type", strNode("http"),
			"server", strNode("127.0.0.1"),
			"port", intNode(cp.AgentPort),
			"username", strNode(s.User),
			"password", strNode(s.Pass),
		)))
	}
	ls := c.seq("listeners")
	removeNamed(ls, CaptureUpListener)
	ls.Content = append(ls.Content, c.mark(mapNode(
		"name", strNode(CaptureUpListener),
		"type", strNode("mixed"),
		"listen", strNode("127.0.0.1"),
		"port", intNode(cp.UpPort),
		"udp", boolNode(false),
		"users", seqNode(mapNode("username", strNode(cp.UpUser), "password", strNode(cp.UpPass))),
	)))

	rp := c.sub("rule-providers")
	for i := 0; i+1 < len(rp.Content); {
		if strings.HasPrefix(rp.Content[i].Value, captureNamePrefix) {
			rp.Content = append(rp.Content[:i], rp.Content[i+2:]...)
			continue
		}
		i += 2
	}
	bypass := captureRuleLines(cp.Bypass)
	if len(bypass) > 0 {
		mapSet(rp, captureBypassSet, c.mark(inlineProvider(bypass)))
	}

	// ② ¬return ∧ TCP ∧ port ∧ local source ∧ ¬bypass ∧ selected; the
	// selection comes first so unselected connections stop there (an
	// IP-CIDR in the bypass list would otherwise resolve every name).
	common := "(NOT,((IN-NAME," + CaptureUpListener + "))),(NETWORK,TCP),(DST-PORT," + ports + ")," +
		"(OR,((SRC-IP-CIDR,127.0.0.0/8),(SRC-IP-CIDR,::1/128),(IN-TYPE,TUN)))"
	if !cp.IPv6 {
		// IPv6 is refused further down (ipv6RejectRule): keep literals out of
		// the agent so the client falls back at once (docs/capture.md §13)
		common += ",(NOT,((IP-CIDR6,::/0,no-resolve)))"
	}
	if len(bypass) > 0 {
		common += ",(NOT,((RULE-SET," + captureBypassSet + ")))"
	}
	// the private-network entries apply to every selection except the
	// domain patterns allowed onto intranet destinations
	lanCommon := common
	if priv := captureRuleLines(cp.Private); len(priv) > 0 {
		mapSet(rp, capturePrivSet, c.mark(inlineProvider(priv)))
		common += ",(NOT,((RULE-SET," + capturePrivSet + ")))"
	}
	var rules []*yaml.Node
	for i, a := range cp.Apps {
		if len(a.Rules) == 0 {
			continue
		}
		set := fmt.Sprintf("%s%d", captureAppSet, i)
		mapSet(rp, set, c.mark(inlineProvider(a.Rules)))
		rules = append(rules, c.mark(strNode("AND,((RULE-SET,"+set+"),"+common+"),"+a.Outbound)))
	}
	if len(cp.Domains) > 0 && cp.Domain.Outbound != "" {
		lan, rest := splitAllowPrivate(cp.Domains, cp.AllowPrivate)
		// the intranet-allowed selection first: a host matching both keeps
		// the exemption the user asked for
		if l := captureRuleLines(lan); len(l) > 0 {
			mapSet(rp, captureSelLANSet, c.mark(inlineProvider(l)))
			rules = append(rules, c.mark(strNode("AND,((RULE-SET,"+captureSelLANSet+"),"+lanCommon+"),"+cp.Domain.Outbound)))
		}
		if r := captureRuleLines(rest); len(r) > 0 {
			mapSet(rp, captureSelSet, c.mark(inlineProvider(r)))
			rules = append(rules, c.mark(strNode("AND,((RULE-SET,"+captureSelSet+"),"+common+"),"+cp.Domain.Outbound)))
		}
	}
	rs := c.seq("rules")
	rs.Content = append(rules, rs.Content...)
}

// splitAllowPrivate separates the selections allowed onto intranet
// destinations from the others (comparison ignores case and a final dot).
func splitAllowPrivate(domains, allow []string) (lan, rest []string) {
	norm := func(s string) string { return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), ".")) }
	ok := map[string]bool{}
	for _, a := range allow {
		ok[norm(a)] = true
	}
	for _, d := range domains {
		if ok[norm(d)] {
			lan = append(lan, d)
		} else {
			rest = append(rest, d)
		}
	}
	return lan, rest
}

// hoistCaptureRules moves the capture rules in front of the coexistence
// layer's domain and address DIRECT rules, which applyCoexist put first:
// a host the user chose to decrypt is captured, then its return leg
// (lanflow-mitm-up, excluded from the capture rules) meets those DIRECT
// rules and leaves exactly as before. The VPN's own processes stay first:
// their traffic is never decrypted.
func hoistCaptureRules(c *composer) {
	rs := deref(mapGet(c.root, "rules"))
	if rs == nil || rs.Kind != yaml.SequenceNode {
		return
	}
	var capt, rest []*yaml.Node
	for _, n := range rs.Content {
		if c.nodeSrc[n] == layerCapture {
			capt = append(capt, n)
		} else {
			rest = append(rest, n)
		}
	}
	if len(capt) == 0 {
		return
	}
	at := 0
	for at < len(rest) && c.nodeSrc[rest[at]] == layerCoexist && strings.HasPrefix(rest[at].Value, "PROCESS-NAME,") {
		at++
	}
	out := make([]*yaml.Node, 0, len(rs.Content))
	out = append(out, rest[:at]...)
	out = append(out, capt...)
	out = append(out, rest[at:]...)
	rs.Content = out
}

// scrubCaptureRefs removes every reference to a decrypting outbound, the
// return listener or the capture rule sets that does not come from the
// capture layer (docs/capture.md §3.3 step 4) and returns a warning per
// removal.
func scrubCaptureRefs(c *composer) []string {
	var warns []string
	ours := func(n *yaml.Node) bool { return c.nodeSrc[n] == layerCapture }
	mentions := func(s string) bool {
		u := strings.ToUpper(s)
		return strings.Contains(u, CaptureOutboundPrefix)
	}
	warn := func(layer, format string, args ...any) {
		if layer == "" {
			layer = layerSub
		}
		warns = append(warns, layer+"："+fmt.Sprintf(format, args...))
	}
	isCaptureName := func(s string) bool {
		return strings.HasPrefix(strings.ToUpper(s), CaptureOutboundPrefix) || strings.HasPrefix(strings.ToLower(s), captureNamePrefix)
	}

	filterRules := func(seq *yaml.Node, where string) {
		seq = deref(seq)
		if seq == nil || seq.Kind != yaml.SequenceNode {
			return
		}
		out := seq.Content[:0]
		for _, it := range seq.Content {
			if !ours(it) && it.Kind == yaml.ScalarNode && mentions(it.Value) {
				warn(c.nodeSrc[it], "已移除引用解密出站的%s「%s」", where, it.Value)
				continue
			}
			out = append(out, it)
		}
		seq.Content = out
	}
	filterRules(mapGet(c.root, "rules"), "规则")
	if sr := deref(mapGet(c.root, "sub-rules")); sr != nil && sr.Kind == yaml.MappingNode {
		for i := 1; i < len(sr.Content); i += 2 {
			filterRules(sr.Content[i], "子规则")
		}
	}

	// proxies, groups, listeners, tunnels and providers named like ours
	for _, key := range []string{"proxies", "proxy-groups", "listeners"} {
		seq := deref(mapGet(c.root, key))
		if seq == nil || seq.Kind != yaml.SequenceNode {
			continue
		}
		out := seq.Content[:0]
		for _, it := range seq.Content {
			m := deref(it)
			name := ""
			if n := deref(mapGet(m, "name")); n != nil {
				name = n.Value
			}
			if !ours(it) && isCaptureName(name) {
				warn(c.nodeSrc[it], "已移除与解密层同名的「%s」", name)
				continue
			}
			if !ours(it) && m != nil && m.Kind == yaml.MappingNode {
				if key == "listeners" {
					if p := deref(mapGet(m, "proxy")); p != nil && mentions(p.Value) {
						warn(c.nodeSrc[it], "已移除指向解密出站的入站「%s」", name)
						continue
					}
				}
				if key == "proxies" {
					if p := deref(mapGet(m, "dialer-proxy")); p != nil && mentions(p.Value) {
						mapDelete(m, "dialer-proxy")
						warn(c.nodeSrc[it], "已移除节点「%s」指向解密出站的 dialer-proxy", name)
					}
				}
				if key == "proxy-groups" {
					if ps := deref(mapGet(m, "proxies")); ps != nil && ps.Kind == yaml.SequenceNode {
						kept := ps.Content[:0]
						for _, p := range ps.Content {
							if mentions(p.Value) {
								warn(c.nodeSrc[it], "已从代理组「%s」移除解密出站", name)
								continue
							}
							kept = append(kept, p)
						}
						ps.Content = kept
					}
				}
			}
			out = append(out, it)
		}
		seq.Content = out
	}
	if tn := deref(mapGet(c.root, "tunnels")); tn != nil && tn.Kind == yaml.SequenceNode {
		out := tn.Content[:0]
		for _, it := range tn.Content {
			drop := false
			if m := deref(it); m != nil && m.Kind == yaml.MappingNode {
				if p := deref(mapGet(m, "proxy")); p != nil && mentions(p.Value) {
					drop = true
				}
			} else if it.Kind == yaml.ScalarNode && mentions(it.Value) { // "tcp,127.0.0.1:6553,host:53,proxy"
				drop = true
			}
			if drop && !ours(it) {
				warn(c.nodeSrc[it], "已移除指向解密出站的隧道")
				continue
			}
			out = append(out, it)
		}
		tn.Content = out
	}
	if rp := deref(mapGet(c.root, "rule-providers")); rp != nil && rp.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(rp.Content); {
			if strings.HasPrefix(strings.ToLower(rp.Content[i].Value), captureNamePrefix) && !ours(rp.Content[i+1]) {
				warn(c.nodeSrc[rp.Content[i+1]], "已移除与解密层同名的规则集「%s」", rp.Content[i].Value)
				rp.Content = append(rp.Content[:i], rp.Content[i+2:]...)
				continue
			}
			i += 2
		}
	}

	// groups that collect proxies by themselves: exclude ours, appending to
	// any exclude-filter already there (Mihomo separates regexes with `)
	if gs := deref(mapGet(c.root, "proxy-groups")); gs != nil && gs.Kind == yaml.SequenceNode {
		for _, it := range gs.Content {
			m := deref(it)
			if m == nil || m.Kind != yaml.MappingNode {
				continue
			}
			if !truthy(mapGet(m, "include-all")) && !truthy(mapGet(m, "include-all-proxies")) && deref(mapGet(m, "filter")) == nil {
				continue
			}
			const ex = "^" + CaptureOutboundPrefix
			if old := deref(mapGet(m, "exclude-filter")); old != nil && old.Kind == yaml.ScalarNode && old.Value != "" {
				if !strings.Contains(old.Value, ex) {
					mapSet(m, "exclude-filter", strNode(old.Value+"`"+ex))
				}
			} else {
				mapSet(m, "exclude-filter", strNode(ex))
			}
		}
	}
	return warns
}

// captureFindProcess keeps the kernel able to tell programs apart while a
// program selection is active (docs/capture.md §4.1): find-process-mode
// "off" (from 高级 or an override) becomes "strict", with a warning.
func captureFindProcess(c *composer, cp Capture) []string {
	if !cp.active() || len(cp.Apps) == 0 {
		return nil
	}
	if v := deref(mapGet(c.root, "find-process-mode")); v != nil && strings.EqualFold(v.Value, "off") {
		c.layer = layerCapture
		c.set("find-process-mode", strNode("strict"))
		return []string{layerCapture + "：按程序解密需要识别进程，find-process-mode 已由 off 临时改为 strict"}
	}
	return nil
}

func truthy(n *yaml.Node) bool {
	n = deref(n)
	return n != nil && n.Kind == yaml.ScalarNode && (n.Value == "true" || n.Value == "True" || n.Value == "yes")
}
