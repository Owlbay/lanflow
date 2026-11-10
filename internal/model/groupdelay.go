package model

// GroupNodes returns the nodes a delay test of name covers: name itself
// when it is a node, else every node reachable through the group and the
// groups nested in it, each once, in member order. DIRECT and REJECT are
// left out; a nesting cycle is followed once.
func (s ProxySnapshot) GroupNodes(name string) []string {
	groups := make(map[string]ProxyGroup, len(s.Groups))
	for _, g := range s.Groups {
		groups[g.Name] = g
	}
	var out []string
	seen := map[string]bool{}
	var walk func(n string)
	walk = func(n string) {
		if seen[n] {
			return
		}
		seen[n] = true
		g, isGroup := groups[n]
		if !isGroup {
			if n != "DIRECT" && n != "REJECT" {
				out = append(out, n)
			}
			return
		}
		for _, m := range g.All {
			walk(m)
		}
	}
	walk(name)
	return out
}

// FastestDelay returns the delay a group shows: that of its fastest tested
// node (nested groups included), -1 when every tested node timed out, 0
// when none was tested; testing reports whether any of them is being
// tested. For a node it returns the node's own delay.
func (s ProxySnapshot) FastestDelay(name string) (delay int, testing bool) {
	for _, n := range s.GroupNodes(name) {
		p := s.Proxies[n]
		testing = testing || p.Testing
		switch {
		case p.Delay > 0 && (delay <= 0 || p.Delay < delay):
			delay = p.Delay
		case p.Delay < 0 && delay == 0:
			delay = -1
		}
	}
	return delay, testing
}

// ApplyGroupDelays sets every group's Delay and Testing in s.Proxies to
// FastestDelay, so everything showing a group's delay (the proxies page,
// the tray, ctl, MCP) shows its fastest node rather than whichever member
// happens to be selected.
func (s ProxySnapshot) ApplyGroupDelays() {
	for _, g := range s.Groups {
		p, ok := s.Proxies[g.Name]
		if !ok {
			p = Proxy{Name: g.Name}
		}
		p.Delay, p.Testing = s.FastestDelay(g.Name)
		s.Proxies[g.Name] = p
	}
}
