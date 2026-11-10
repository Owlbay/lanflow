package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"lanflow/internal/app/remote"
	"lanflow/internal/ctl"
	"lanflow/internal/model"
)

// Composite tools: the sequences agents need most, done in one call with
// the standard flow (look → preview → apply → verify).

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func boolean(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func compositeTools() []tool {
	return []tool{
		{Tool: Tool{Name: "route_domain", Title: "Route a domain through a node or group",
			Description: "Make a domain go through a proxy node, a proxy group, DIRECT or REJECT: tests which rule it hits now, adds (or updates) a custom rule placed first, previews the rule list diff, applies it unless dry_run, then verifies with a rule test and a delay test of the exit node. [write: the first write asks the user in Lanflow; revert with undo]",
			InputSchema: obj(map[string]any{
				"domain":    str("Domain (or URL) to route, e.g. openai.com"),
				"target":    str("Name of a proxy node or proxy group (see proxies), or DIRECT / REJECT"),
				"rule_type": str("DOMAIN-SUFFIX (default: the domain and its subdomains), DOMAIN (exactly this name) or DOMAIN-KEYWORD"),
				"dry_run":   boolean("Only show what would change"),
			}, "domain", "target")},
			composite: routeDomain},
		{Tool: Tool{Name: "diagnose_site", Title: "Diagnose why a website does not load",
			Description: "Explain why a website may not load through Lanflow: core state, which rule and proxy the host takes, DNS answer, delay test of the exit node, with findings in plain words. Read-only except for the delay test.",
			InputSchema: obj(map[string]any{"host": str("Domain, host:port or URL of the site")}, "host"),
			Annotations: map[string]any{"readOnlyHint": true}},
			composite: func(_ context.Context, c *remote.Client, a map[string]json.RawMessage) (any, error) {
				host := argStr(a, "host")
				if host == "" {
					return nil, errors.New("host is required")
				}
				return ctl.DiagnoseSite(c, host)
			}},
		{Tool: Tool{Name: "diagnose", Title: "Overall diagnosis",
			Description: "Snapshot of Lanflow's state with findings: core, system proxy/TUN, mode, subscription, network, DNS in force, site connectivity, DNS/IPv6 leak check, recent warnings. With run=true the site and leak tests run first (takes a few seconds).",
			InputSchema: obj(map[string]any{"run": boolean("Run the site connectivity and leak tests first")}),
			Annotations: map[string]any{"readOnlyHint": true}},
			composite: func(_ context.Context, c *remote.Client, a map[string]json.RawMessage) (any, error) {
				return ctl.Diagnose(c, argBool(a, "run"))
			}},
		{Tool: Tool{Name: "switch_node", Title: "Switch node and test its delay",
			Description: "Select a node in a proxy group and measure it. Without node, tests every member of the group and picks the fastest. Returns the previous and new selection, the delay and the fastest members. [write: the first write asks the user in Lanflow; revert with undo]",
			InputSchema: obj(map[string]any{
				"group": str("Proxy group to change (a select group, see proxies); default: the main group"),
				"node":  str("Member to select; empty = the fastest after a delay test"),
			})},
			composite: switchNode},
	}
}

func argStr(a map[string]json.RawMessage, k string) string {
	var s string
	if v, ok := a[k]; ok {
		_ = json.Unmarshal(v, &s)
	}
	return strings.TrimSpace(s)
}

func argBool(a map[string]json.RawMessage, k string) bool {
	var b bool
	if v, ok := a[k]; ok {
		_ = json.Unmarshal(v, &b)
	}
	return b
}

// RouteResult is the result of route_domain.
type RouteResult struct {
	Before   model.RuleMatch  `json:"before"`
	Rule     model.CustomRule `json:"rule"`
	Diff     []string         `json:"diff"`
	Applied  bool             `json:"applied"`
	After    *model.RuleMatch `json:"after,omitempty"`
	Verified bool             `json:"verified"` // the domain now hits the new rule and goes to target
	Exit     string           `json:"exit,omitempty"`
	Delay    *int             `json:"delay,omitempty"` // exit node delay, ms; -1 failed
	Notes    []string         `json:"notes,omitempty"`
}

func routeDomain(_ context.Context, c *remote.Client, a map[string]json.RawMessage) (any, error) {
	domain := ctl.CleanHost(argStr(a, "domain"))
	target := argStr(a, "target")
	if domain == "" || target == "" {
		return nil, errors.New("domain and target are required")
	}
	typ := strings.ToUpper(argStr(a, "rule_type"))
	if typ == "" {
		typ = "DOMAIN-SUFFIX"
	}
	if typ != "DOMAIN-SUFFIX" && typ != "DOMAIN" && typ != "DOMAIN-KEYWORD" {
		return nil, fmt.Errorf("rule_type must be DOMAIN-SUFFIX, DOMAIN or DOMAIN-KEYWORD")
	}
	// 1. The target must exist.
	px := c.Proxies()
	if err := c.LastError(); err != nil {
		return nil, err
	}
	up := strings.ToUpper(target)
	if up == "DIRECT" || up == "REJECT" {
		target = up
	} else if _, ok := px.Proxies[target]; !ok {
		return nil, fmt.Errorf("no node or group named %q; call proxies to see the names", target)
	}
	// 2. Where it goes now.
	before, err := c.TestRule(domain)
	if err != nil {
		return nil, err
	}
	res := RouteResult{Before: before}
	// 3. The rule: update an existing custom rule for the same domain
	// rather than stacking a second one.
	rule := model.CustomRule{Type: typ, Payload: domain, Target: target}
	cur := c.CustomRules()
	if err := c.LastError(); err != nil {
		return nil, err
	}
	rest := cur
	for i, r := range cur {
		if r.Type == typ && strings.EqualFold(r.Payload, domain) {
			if r.Target == target {
				res.Notes = append(res.Notes, "an identical custom rule already exists; nothing to change")
				res.Rule, res.Verified = r, before.Custom && before.Policy == target
				res.After = &before
				return res, nil
			}
			rule.ID = r.ID
			res.Notes = append(res.Notes, fmt.Sprintf("updating the existing custom rule (was → %s)", r.Target))
			rest = append(append([]model.CustomRule(nil), cur[:i]...), cur[i+1:]...)
			res.Diff = []string{"- " + r.Type + "," + r.Payload + "," + r.Target}
		}
	}
	res.Rule = rule
	res.Diff = append(res.Diff, ctl.DiffText(ctl.RulesDiff(rest, rule))...)
	if argBool(a, "dry_run") {
		return res, nil
	}
	// 4. Apply.
	saved, err := c.SaveCustomRule(rule)
	if err != nil {
		return nil, err
	}
	res.Rule, res.Applied = saved, true
	// 5. Verify: the rule test, then the exit.
	after, err := c.TestRule(domain)
	if err != nil {
		res.Notes = append(res.Notes, "rule test after applying failed: "+err.Error())
		return res, nil
	}
	res.After = &after
	inChain := after.Policy == target
	for _, h := range after.Chain {
		inChain = inChain || h == target
	}
	res.Verified = after.Custom && inChain
	if !res.Verified {
		res.Notes = append(res.Notes, "the domain does not hit the new rule yet: an earlier rule (or the core reload) may take precedence")
	}
	if n := len(after.Chain); n > 0 {
		res.Exit = after.Chain[n-1]
	} else {
		res.Exit = target
	}
	if res.Exit != "DIRECT" && res.Exit != "REJECT" {
		if err := c.TestProxyDelay(res.Exit); err != nil {
			res.Notes = append(res.Notes, "delay test failed: "+err.Error())
		} else {
			d := c.Proxies().Proxies[res.Exit].Delay
			res.Delay = &d
			if d < 0 {
				res.Notes = append(res.Notes, fmt.Sprintf("the exit node %q did not answer the delay test: the site may not load; consider another node", res.Exit))
			}
		}
	}
	res.Notes = append(res.Notes, "call undo to revert this rule")
	return res, nil
}

// SwitchResult is the result of switch_node.
type SwitchResult struct {
	Group    string           `json:"group"`
	Previous string           `json:"previous"`
	Selected string           `json:"selected"`
	Delay    int              `json:"delay"` // ms; -1 failed
	Fastest  []ctl.MemberView `json:"fastest,omitempty"`
}

func switchNode(_ context.Context, c *remote.Client, a map[string]json.RawMessage) (any, error) {
	px := c.Proxies()
	if err := c.LastError(); err != nil {
		return nil, err
	}
	group := argStr(a, "group")
	if group == "" {
		group = px.MainGroup
	}
	var g *model.ProxyGroup
	for i := range px.Groups {
		if px.Groups[i].Name == group {
			g = &px.Groups[i]
		}
	}
	if g == nil {
		return nil, fmt.Errorf("no proxy group named %q", group)
	}
	if g.Type != model.GroupSelect {
		return nil, fmt.Errorf("%q is a %s group: only select groups can be switched", group, g.Type)
	}
	res := SwitchResult{Group: group, Previous: g.Now}
	node := argStr(a, "node")
	if node == "" {
		if err := c.TestGroupDelay(group); err != nil {
			return nil, err
		}
		px = c.Proxies()
		var ms []ctl.MemberView
		for _, m := range g.All {
			p := px.Proxies[m]
			if p.Delay > 0 {
				ms = append(ms, ctl.MemberView{Name: m, Type: p.Type, Delay: p.Delay})
			}
		}
		if len(ms) == 0 {
			return nil, fmt.Errorf("no member of %q answered the delay test", group)
		}
		sort.SliceStable(ms, func(i, j int) bool { return ms[i].Delay < ms[j].Delay })
		node = ms[0].Name
		if len(ms) > 5 {
			ms = ms[:5]
		}
		res.Fastest = ms
	} else {
		found := false
		for _, m := range g.All {
			found = found || m == node
		}
		if !found {
			return nil, fmt.Errorf("%q is not a member of %q", node, group)
		}
	}
	if err := c.SelectProxy(group, node); err != nil {
		return nil, err
	}
	res.Selected = node
	if err := c.TestProxyDelay(node); err == nil {
		res.Delay = c.Proxies().Proxies[node].Delay
	}
	return res, nil
}
