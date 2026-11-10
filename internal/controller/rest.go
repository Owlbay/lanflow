package controller

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// GET /version → {"meta": true, "version": "v1.x.y"}
func (c *client) version(ctx context.Context) (string, error) {
	var out struct {
		Meta    bool   `json:"meta"`
		Version string `json:"version"`
	}
	err := c.do(ctx, request{method: http.MethodGet, path: "/version", out: &out, op: "获取内核版本失败"})
	return out.Version, err
}

// GET /configs → config.General (we decode the subset in Configs).
func (c *client) configs(ctx context.Context) (Configs, error) {
	var out Configs
	err := c.do(ctx, request{method: http.MethodGet, path: "/configs", out: &out, op: "读取内核配置失败"})
	return out, err
}

// PATCH /configs with only the non-nil fields (Mihomo treats absent keys
// as "unchanged").
func (c *client) patchConfigs(ctx context.Context, p ConfigPatch) error {
	return c.do(ctx, request{method: http.MethodPatch, path: "/configs", body: p, op: "修改内核配置失败"})
}

// PUT /configs?force=true {"path": path}. The kernel requires an absolute
// path inside its safe-path set; an empty path reloads its default file.
func (c *client) reload(ctx context.Context, path string) error {
	body := struct {
		Path string `json:"path"`
	}{Path: path}
	// Parsing + applying a large profile may take a while; give it more
	// headroom than ordinary calls.
	return c.do(ctx, request{
		method: http.MethodPut, path: "/configs",
		query: url.Values{"force": {"true"}},
		body:  body, op: "重载配置失败", timeout: 60 * time.Second,
	})
}

// POST /configs/geo — the kernel downloads GeoIP/GeoSite/MMDB/ASN files.
func (c *client) updateGeo(ctx context.Context) error {
	return c.do(ctx, request{method: http.MethodPost, path: "/configs/geo", op: "更新 Geo 数据失败", timeout: 5 * time.Minute})
}

// GET /proxies → {"proxies": {name: Proxy}}
func (c *client) proxies(ctx context.Context) (map[string]Proxy, error) {
	var out struct {
		Proxies map[string]Proxy `json:"proxies"`
	}
	err := c.do(ctx, request{method: http.MethodGet, path: "/proxies", out: &out, op: "获取节点列表失败"})
	return out.Proxies, err
}

// PUT /proxies/{group} {"name": name}
func (c *client) selectProxy(ctx context.Context, group, name string) error {
	body := struct {
		Name string `json:"name"`
	}{Name: name}
	return c.do(ctx, request{method: http.MethodPut, path: "/proxies/" + escape(group), body: body, op: "切换节点失败"})
}

// delayQuery builds ?url=&timeout=<ms>.
func delayQuery(testURL string, timeout time.Duration) url.Values {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return url.Values{
		"url":     {testURL},
		"timeout": {strconv.FormatInt(timeout.Milliseconds(), 10)},
	}
}

// GET /proxies/{name}/delay?url=&timeout= → {"delay": ms}
func (c *client) proxyDelay(ctx context.Context, name, testURL string, timeout time.Duration) (int, error) {
	var out struct {
		Delay int `json:"delay"`
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	err := c.do(ctx, request{
		method: http.MethodGet, path: "/proxies/" + escape(name) + "/delay",
		query: delayQuery(testURL, timeout), out: &out,
		op: "延迟测试失败", timeout: timeout + delayExtra,
	})
	return out.Delay, err
}

// GET /group/{name}/delay?url=&timeout= → {member: ms, ...}
func (c *client) groupDelay(ctx context.Context, group, testURL string, timeout time.Duration) (map[string]int, error) {
	out := map[string]int{}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	err := c.do(ctx, request{
		method: http.MethodGet, path: "/group/" + escape(group) + "/delay",
		query: delayQuery(testURL, timeout), out: &out,
		op: "分组延迟测试失败", timeout: timeout + delayExtra,
	})
	return out, err
}

// GET /rules → {"rules": [Rule]}
func (c *client) rules(ctx context.Context) ([]Rule, error) {
	var out struct {
		Rules []Rule `json:"rules"`
	}
	err := c.do(ctx, request{method: http.MethodGet, path: "/rules", out: &out, op: "获取规则列表失败"})
	return out.Rules, err
}

// PATCH /rules/disable with a JSON object {"<index>": disabled}. Mihomo
// decodes it into map[int]bool, so keys are decimal strings.
func (c *client) disableRules(ctx context.Context, m map[int]bool) error {
	if len(m) == 0 {
		return nil
	}
	body := make(map[string]bool, len(m))
	for idx, disabled := range m {
		body[strconv.Itoa(idx)] = disabled
	}
	return c.do(ctx, request{method: http.MethodPatch, path: "/rules/disable", body: body, op: "切换规则状态失败"})
}

// GET /providers/rules → {"providers": {name: RuleProvider}}
func (c *client) ruleProviders(ctx context.Context) (map[string]RuleProvider, error) {
	var out struct {
		Providers map[string]RuleProvider `json:"providers"`
	}
	err := c.do(ctx, request{method: http.MethodGet, path: "/providers/rules", out: &out, op: "获取规则集列表失败"})
	return out.Providers, err
}

// PUT /providers/rules/{name} — downloads the rule set again.
func (c *client) updateRuleProvider(ctx context.Context, name string) error {
	return c.do(ctx, request{method: http.MethodPut, path: "/providers/rules/" + escape(name), op: "更新规则集失败", timeout: 60 * time.Second})
}

// GET /connections → Connections snapshot
func (c *client) connections(ctx context.Context) (Connections, error) {
	var out Connections
	err := c.do(ctx, request{method: http.MethodGet, path: "/connections", out: &out, op: "获取连接列表失败"})
	return out, err
}

// DELETE /connections/{id}
func (c *client) closeConn(ctx context.Context, id string) error {
	return c.do(ctx, request{method: http.MethodDelete, path: "/connections/" + escape(id), op: "关闭连接失败"})
}

// DELETE /connections
func (c *client) closeAll(ctx context.Context) error {
	return c.do(ctx, request{method: http.MethodDelete, path: "/connections", op: "关闭全部连接失败"})
}

// GET /dns/query?name=&type=
func (c *client) dnsQuery(ctx context.Context, name, qtype string) (DNSResult, error) {
	if qtype == "" {
		qtype = "A"
	}
	var out DNSResult
	err := c.do(ctx, request{
		method: http.MethodGet, path: "/dns/query",
		query: url.Values{"name": {name}, "type": {qtype}},
		out:   &out, op: "DNS 查询失败",
	})
	return out, err
}

// POST /cache/dns/flush or /cache/fakeip/flush
func (c *client) flush(ctx context.Context, path string) error {
	op := "清空 DNS 缓存失败"
	if path == "/cache/fakeip/flush" {
		op = "清空 Fake-IP 缓存失败"
	}
	return c.do(ctx, request{method: http.MethodPost, path: path, op: op})
}
