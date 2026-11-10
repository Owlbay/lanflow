package controller

import (
	"context"
	"net/http"
	"time"
)

// Proxy-provider endpoints (hub/route/provider.go in the Mihomo source):
//
//	GET /providers/proxies                     → {"providers": {name: ProxyProvider}}
//	PUT /providers/proxies/{name}              → 204, re-downloads the provider
//	GET /providers/proxies/{name}/healthcheck  → 204 once every member was tested

// SubscriptionInfo is the traffic/expiry information a provider's
// subscription-userinfo header carried (adapter/provider/subscription_info.go;
// the struct has no JSON tags, so the keys are the field names).
type SubscriptionInfo struct {
	Upload   int64 `json:"Upload"`
	Download int64 `json:"Download"`
	Total    int64 `json:"Total"`
	Expire   int64 `json:"Expire"` // unix seconds, 0 = none
}

// ProxyProvider is an entry of GET /providers/proxies (providerForApi in
// adapter/provider/provider.go).
type ProxyProvider struct {
	Name             string            `json:"name"`
	Type             string            `json:"type"`        // "Proxy"
	VehicleType      string            `json:"vehicleType"` // "HTTP", "File", "Inline", "Compatible"
	Proxies          []Proxy           `json:"proxies"`
	TestURL          string            `json:"testUrl"`
	ExpectedStatus   string            `json:"expectedStatus"`
	UpdatedAt        time.Time         `json:"updatedAt"`
	SubscriptionInfo *SubscriptionInfo `json:"subscriptionInfo,omitempty"`
}

// providerTimeout covers a provider download or a full health check (the
// kernel tests members 10 at a time with the provider's own timeout).
const providerTimeout = 90 * time.Second

// GET /providers/proxies → {"providers": {name: ProxyProvider}}
func (c *client) proxyProviders(ctx context.Context) (map[string]ProxyProvider, error) {
	var out struct {
		Providers map[string]ProxyProvider `json:"providers"`
	}
	err := c.do(ctx, request{method: http.MethodGet, path: "/providers/proxies", out: &out, op: "获取代理集合列表失败"})
	return out.Providers, err
}

// PUT /providers/proxies/{name} — downloads the provider again.
func (c *client) updateProxyProvider(ctx context.Context, name string) error {
	return c.do(ctx, request{method: http.MethodPut, path: "/providers/proxies/" + escape(name), op: "更新代理集合失败", timeout: providerTimeout})
}

// GET /providers/proxies/{name}/healthcheck — tests every member; the
// kernel answers when all are done.
func (c *client) healthCheckProvider(ctx context.Context, name string) error {
	return c.do(ctx, request{method: http.MethodGet, path: "/providers/proxies/" + escape(name) + "/healthcheck", op: "代理集合健康检查失败", timeout: providerTimeout})
}

// ProxyProviders does GET /providers/proxies.
func (c *Client) ProxyProviders(ctx context.Context) (map[string]ProxyProvider, error) {
	return c.c.proxyProviders(ctx)
}

// UpdateProxyProvider does PUT /providers/proxies/{name}.
func (c *Client) UpdateProxyProvider(ctx context.Context, name string) error {
	return c.c.updateProxyProvider(ctx, name)
}

// HealthCheckProvider does GET /providers/proxies/{name}/healthcheck and
// returns once every member of the provider has been tested.
func (c *Client) HealthCheckProvider(ctx context.Context, name string) error {
	return c.c.healthCheckProvider(ctx, name)
}
