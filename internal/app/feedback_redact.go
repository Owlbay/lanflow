package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"lanflow/internal/applog"
)

// What a feedback report must not carry (docs/feedback-api.md §隐私):
// on top of the diagnostics redaction (servers, credentials, link paths
// and tokens removed: applog.Redact, redactConfig) a report drops domain
// lists, connection lines and Wi-Fi names, and masks every IP address
// outside private, loopback, link-local and Mihomo's fake-ip ranges.

const feedbackTailBytes = 512 << 10 // at most the last 512 KB of each log

const feedbackReadme = `Lanflow feedback diagnostics

system.txt    app and core state, network info, subscriptions (host only)
config/       Lanflow settings, the runtime config and overrides, without
              rules, hosts, bypass lists, DNS policies and Wi-Fi names
logs/         core warnings and errors (no connection lines), helper logs,
              and Lanflow's log when it was chosen
network/      routes, DNS resolvers and the system proxy as the OS reports them

Proxy servers, passwords, UUIDs and keys are removed; links keep only their
host; token / password values are masked; public IP addresses are masked.
`

var (
	ipv4Re = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	// candidates only: netip decides
	ipv6Re = regexp.MustCompile(`(?i)\b[0-9a-f]{0,4}(?::[0-9a-f]{0,4}){2,7}(?:%[0-9a-z]+)?`)
)

// Mihomo's fake-ip and benchmark ranges are not anyone's address.
var keptPrefixes = []netip.Prefix{
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT, shared address space
	netip.MustParsePrefix("fdfe:dcba:9876::/48"),
}

// keepIP reports whether a stays readable in a report: addresses of the
// local network and special ranges, not public ones.
func keepIP(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsUnspecified() || a.IsMulticast() || a.IsInterfaceLocalMulticast() {
		return true
	}
	if a.Is4() && (a.As4()[0] == 255 || a.As4()[0] == 0) {
		return true
	}
	for _, p := range keptPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// maskPublicIPs replaces every public IPv4 / IPv6 address in s.
func maskPublicIPs(s string) string {
	s = ipv4Re.ReplaceAllStringFunc(s, func(m string) string {
		a, err := netip.ParseAddr(m)
		if err != nil || keepIP(a) {
			return m
		}
		return "x.x.x.x"
	})
	return ipv6Re.ReplaceAllStringFunc(s, func(m string) string {
		if strings.Count(m, ":") < 2 {
			return m
		}
		a, err := netip.ParseAddr(m)
		if err != nil || !a.Is6() || keepIP(a) {
			return m
		}
		return "x:x:x:x"
	})
}

// redactReport is applied to every text of a report.
func redactReport(s string) string { return maskPublicIPs(applog.Redact(s)) }

// privateKeys are config and settings keys whose values are lists of
// domains, addresses, names or credentials: a report keeps only how many
// items they had. Compared lowercased without '-' and '_'.
var privateKeys = map[string]bool{
	"rules": true, "subrules": true, "ruleproviders": true, "payload": true,
	"hosts": true, "nameserverpolicy": true, "fakeipfilter": true,
	"skipdomain": true, "forcedomain": true, "bypass": true, "sites": true,
	"ssidrules": true, "ssiddefaultsub": true, "atrust": true, "ui": true,
	"agentgrants": true, "connhiddencolumns": true, "webdav": true, "customrules": true,
	"authentication": true, "skipsrcaddress": true, "skipdstaddress": true,
	"routeaddress": true, "routeexcludeaddress": true, "includeuid": true,
	"includepackage": true, "excludepackage": true,
}

func privateKey(k string) bool {
	k = strings.NewReplacer("-", "", "_", "", "+", "", "!", "").Replace(strings.ToLower(k))
	return privateKeys[k]
}

func omitted(n int) string { return fmt.Sprintf("<omitted: %d items>", n) }

func countOf(v any) int {
	switch x := v.(type) {
	case []any:
		return len(x)
	case map[string]any:
		return len(x)
	case nil:
		return 0
	case string:
		if x == "" {
			return 0
		}
	}
	return 1
}

// dropPrivateJSON replaces the private keys of a JSON document.
func dropPrivateJSON(data []byte) []byte {
	var v any
	if json.Unmarshal(data, &v) != nil {
		return data
	}
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				if privateKey(k) {
					x[k] = omitted(countOf(c))
				} else {
					x[k] = walk(c)
				}
			}
		case []any:
			for i := range x {
				x[i] = walk(x[i])
			}
		}
		return v
	}
	out, err := json.MarshalIndent(walk(v), "", "  ")
	if err != nil {
		return data
	}
	return out
}

// dropPrivateYAML replaces the private keys of a YAML document.
func dropPrivateYAML(data []byte) []byte {
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) == 0 {
		return data
	}
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, c := range n.Content {
				walk(c)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if privateKey(k.Value) {
					cnt := len(v.Content)
					if v.Kind == yaml.MappingNode {
						cnt /= 2
					} else if v.Kind == yaml.ScalarNode && v.Value != "" {
						cnt = 1
					}
					*v = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: omitted(cnt)}
					continue
				}
				walk(v)
			}
		}
	}
	walk(&doc)
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return data
	}
	return out
}

// coreProblems keeps the warning and error lines of a core log, without
// connection lines (source --> destination), which name the sites
// visited.
func coreProblems(data []byte) []byte {
	var out bytes.Buffer
	for _, line := range bytes.Split(data, []byte("\n")) {
		l := string(line)
		if strings.Contains(l, "-->") {
			continue
		}
		lower := strings.ToLower(l)
		if strings.Contains(lower, "level=warn") || strings.Contains(lower, "level=error") ||
			strings.Contains(lower, "level=fatal") || strings.Contains(lower, "panic") {
			out.Write(line)
			out.WriteByte('\n')
		}
	}
	return out.Bytes()
}
