package app

import (
	"strings"

	"gopkg.in/yaml.v3"

	"lanflow/internal/model"
	"lanflow/internal/profile"
)

// The DNS and 嗅探 tabs show, in 跟随订阅 mode, the sections the core
// actually runs: the current subscription's dns / sniffer after the
// overrides (docs/design.md §5.8). Those are parsed here from the stored
// subscription whenever the store caches refresh, so the reads stay cheap.

// EffectiveDNS returns the DNS configuration in force.
func (s *Service) EffectiveDNS() model.EffectiveDNS {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.settings.DNS.TakeOver {
		set := copySettings(s.state.settings)
		rd := parseRuntimeDNS(s.state.runtimeYAML)
		return model.EffectiveDNS{Settings: set.DNS, Source: model.EffectiveLanflow, Enabled: true, Listen: rd.Listen}
	}
	return copyEffectiveDNS(s.state.effDNS)
}

// EffectiveSniffer returns the sniffer configuration in force. The switch
// itself (Enable) is always Lanflow's, which patches it live.
func (s *Service) EffectiveSniffer() model.EffectiveSniffer {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := copySettings(s.state.settings)
	if set.Sniffer.TakeOver {
		return model.EffectiveSniffer{Settings: set.Sniffer, Source: model.EffectiveLanflow}
	}
	out := copyEffectiveSniffer(s.state.effSnf)
	out.Settings.Enable = set.Sniffer.Enable
	return out
}

// subscriptionSections reads the current subscription's dns and sniffer
// sections after the overrides. It reads the store (disk) and must not be
// called with mu held. Without a subscription, or when it cannot be read,
// both fall back to Mihomo's defaults.
func (s *Service) subscriptionSections(cur model.Subscription, hasCur bool) (model.EffectiveDNS, model.EffectiveSniffer) {
	if !hasCur {
		return defaultEffectiveDNS(), defaultEffectiveSniffer()
	}
	data, err := s.d.store.Content(cur.ID)
	if err != nil {
		return defaultEffectiveDNS(), defaultEffectiveSniffer()
	}
	for _, l := range profile.Layers(s.d.store.Overrides(), cur) {
		ov := l.Text
		if strings.TrimSpace(ov) == "" {
			continue
		}
		if merged, err := profile.ApplyOverride(data, ov); err == nil {
			data = merged
		}
	}
	return parseEffectiveSections(data)
}

// defaultEffectiveDNS is what the core runs without a dns section: its
// built-in resolver is off and the system resolves (Mihomo config.go).
func defaultEffectiveDNS() model.EffectiveDNS {
	return model.EffectiveDNS{
		Settings: model.DNSSettings{EnhancedMode: "redir-host", UseSystemHosts: true},
		Source:   model.EffectiveDefault,
	}
}

// defaultEffectiveSniffer is the sniffer section Mihomo assumes when the
// config has none: off, nothing sniffed.
func defaultEffectiveSniffer() model.EffectiveSniffer {
	return model.EffectiveSniffer{Source: model.EffectiveDefault}
}

// rawDNS mirrors the dns keys of a Mihomo config that the DNS tab shows.
type rawDNS struct {
	Enable                bool      `yaml:"enable"`
	Listen                string    `yaml:"listen"`
	IPv6                  bool      `yaml:"ipv6"`
	EnhancedMode          string    `yaml:"enhanced-mode"`
	Nameserver            []string  `yaml:"nameserver"`
	ProxyServerNameserver []string  `yaml:"proxy-server-nameserver"`
	DirectNameserver      []string  `yaml:"direct-nameserver"`
	Policy                yaml.Node `yaml:"nameserver-policy"`
	FakeIPFilter          []string  `yaml:"fake-ip-filter"`
	RespectRules          bool      `yaml:"respect-rules"`
	UseSystemHosts        *bool     `yaml:"use-system-hosts"`
}

// rawSniffer mirrors the sniffer keys of a Mihomo config.
type rawSniffer struct {
	Enable              bool                   `yaml:"enable"`
	OverrideDestination bool                   `yaml:"override-destination"`
	ParsePureIP         bool                   `yaml:"parse-pure-ip"`
	Sniff               map[string]rawSniffOne `yaml:"sniff"`
	ForceDomain         []string               `yaml:"force-domain"`
	SkipDomain          []string               `yaml:"skip-domain"`
	SkipDstAddress      []string               `yaml:"skip-dst-address"`
	// legacy keys (Clash Meta before sniff:)
	Sniffing []string `yaml:"sniffing"`
}

type rawSniffOne struct {
	Ports               []string `yaml:"ports"`
	OverrideDestination *bool    `yaml:"override-destination"`
}

// parseEffectiveSections reads the dns, hosts and sniffer sections of a
// config. A section that is missing or unreadable yields the defaults.
func parseEffectiveSections(data []byte) (model.EffectiveDNS, model.EffectiveSniffer) {
	var doc struct {
		DNS     yaml.Node `yaml:"dns"`
		Hosts   yaml.Node `yaml:"hosts"`
		Sniffer yaml.Node `yaml:"sniffer"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return defaultEffectiveDNS(), defaultEffectiveSniffer()
	}
	return parseDNSSection(&doc.DNS, &doc.Hosts), parseSnifferSection(&doc.Sniffer)
}

func isMapping(n *yaml.Node) bool {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n != nil && n.Kind == yaml.MappingNode && len(n.Content) > 0
}

func parseDNSSection(dnsNode, hostsNode *yaml.Node) model.EffectiveDNS {
	if !isMapping(dnsNode) {
		return defaultEffectiveDNS()
	}
	var raw rawDNS
	if err := dnsNode.Decode(&raw); err != nil {
		return defaultEffectiveDNS()
	}
	d := model.DNSSettings{
		EnhancedMode:          "redir-host",
		Nameserver:            raw.Nameserver,
		ProxyServerNameserver: raw.ProxyServerNameserver,
		DirectNameserver:      raw.DirectNameserver,
		Policy:                kvPairs(&raw.Policy),
		FakeIPFilter:          raw.FakeIPFilter,
		RespectRules:          raw.RespectRules,
		UseSystemHosts:        raw.UseSystemHosts == nil || *raw.UseSystemHosts,
		Hosts:                 kvPairs(hostsNode),
	}
	if strings.EqualFold(strings.TrimSpace(raw.EnhancedMode), "fake-ip") {
		d.EnhancedMode = "fake-ip"
	}
	return model.EffectiveDNS{Settings: d, Source: model.EffectiveSubscription, Enabled: raw.Enable, Listen: strings.TrimSpace(raw.Listen), IPv6: raw.IPv6}
}

// kvPairs flattens a YAML mapping into ordered pairs; list values are
// joined with ", " (nameserver-policy allows several servers per rule).
func kvPairs(n *yaml.Node) []model.KV {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	var out []model.KV
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		for v.Kind == yaml.AliasNode {
			v = v.Alias
		}
		var val string
		switch v.Kind {
		case yaml.ScalarNode:
			val = v.Value
		case yaml.SequenceNode:
			var parts []string
			for _, item := range v.Content {
				if item.Kind == yaml.ScalarNode {
					parts = append(parts, item.Value)
				}
			}
			val = strings.Join(parts, ", ")
		}
		out = append(out, model.KV{Key: k.Value, Value: val})
	}
	return out
}

func parseSnifferSection(n *yaml.Node) model.EffectiveSniffer {
	if !isMapping(n) {
		return defaultEffectiveSniffer()
	}
	var raw rawSniffer
	if err := n.Decode(&raw); err != nil {
		return defaultEffectiveSniffer()
	}
	sn := model.SnifferSettings{
		Enable:              raw.Enable,
		OverrideDestination: raw.OverrideDestination,
		ParsePureIP:         raw.ParsePureIP,
		ForceDomain:         raw.ForceDomain,
		SkipDomain:          raw.SkipDomain,
		SkipDstAddress:      raw.SkipDstAddress,
	}
	for name, one := range raw.Sniff {
		ports := strings.Join(one.Ports, ", ")
		switch strings.ToUpper(name) {
		case "HTTP":
			sn.HTTP, sn.HTTPPorts = true, ports
			if one.OverrideDestination != nil {
				sn.OverrideDestination = *one.OverrideDestination
			}
		case "TLS":
			sn.TLS, sn.TLSPorts = true, ports
		case "QUIC":
			sn.QUIC, sn.QUICPorts = true, ports
		}
	}
	for _, t := range raw.Sniffing {
		switch strings.ToUpper(t) {
		case "HTTP":
			sn.HTTP = true
		case "TLS":
			sn.TLS = true
		case "QUIC":
			sn.QUIC = true
		}
	}
	return model.EffectiveSniffer{Settings: sn, Source: model.EffectiveSubscription}
}

func copyEffectiveDNS(in model.EffectiveDNS) model.EffectiveDNS {
	out := in
	out.Settings.Nameserver = append([]string(nil), in.Settings.Nameserver...)
	out.Settings.ProxyServerNameserver = append([]string(nil), in.Settings.ProxyServerNameserver...)
	out.Settings.DirectNameserver = append([]string(nil), in.Settings.DirectNameserver...)
	out.Settings.Policy = append([]model.KV(nil), in.Settings.Policy...)
	out.Settings.FakeIPFilter = append([]string(nil), in.Settings.FakeIPFilter...)
	out.Settings.Hosts = append([]model.KV(nil), in.Settings.Hosts...)
	return out
}

func copyEffectiveSniffer(in model.EffectiveSniffer) model.EffectiveSniffer {
	out := in
	out.Settings.ForceDomain = append([]string(nil), in.Settings.ForceDomain...)
	out.Settings.SkipDomain = append([]string(nil), in.Settings.SkipDomain...)
	out.Settings.SkipDstAddress = append([]string(nil), in.Settings.SkipDstAddress...)
	return out
}
