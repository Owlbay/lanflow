// Package controller is the Mihomo external-controller client (REST + WebSocket).
//
// Exported signatures are a stable contract. Every endpoint and JSON field
// follows the Mihomo core's REST API (MetaCubeX/mihomo, hub/route).
//
// Rules:
//   - All requests go over the Dial function (unix socket / named pipe);
//     never through the system proxy; the HTTP host is "mihomo".
//   - Every call takes a context and has a default timeout of 10 s
//     (delay tests: the given timeout + 2 s).
//   - Errors are wrapped with a short Chinese description plus the
//     server message; never log secrets.
package controller

import (
	"context"
	"net"
	"time"
)

// DialFunc connects to the controller.
type DialFunc func(ctx context.Context) (net.Conn, error)

// Client talks to one Mihomo controller.
type Client struct{ c *client }

// New returns a client using dial for every connection.
func New(dial DialFunc) *Client { return &Client{newClient(dial)} }

// ---- raw API types (subset of Mihomo's JSON) ----

// DelayHistory is one delay sample.
type DelayHistory struct {
	Time  time.Time `json:"time"`
	Delay int       `json:"delay"`
}

// Proxy is an entry of GET /proxies.
type Proxy struct {
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	UDP      bool           `json:"udp"`
	Now      string         `json:"now,omitempty"`
	All      []string       `json:"all,omitempty"`
	Hidden   bool           `json:"hidden,omitempty"`
	History  []DelayHistory `json:"history"`
	Alive    bool           `json:"alive"`
	Provider string         `json:"provider-name,omitempty"` // verify field name in source
}

// ConfigPatch is the body of PATCH /configs (only non-nil fields are sent).
type ConfigPatch struct {
	Mode      *string `json:"mode,omitempty"`
	LogLevel  *string `json:"log-level,omitempty"`
	AllowLan  *bool   `json:"allow-lan,omitempty"`
	MixedPort *int    `json:"mixed-port,omitempty"`
	Sniffing  *bool   `json:"sniffing,omitempty"`
	IPv6      *bool   `json:"ipv6,omitempty"`
	// 高级 fields the core applies hot (docs/design.md §5.8 高级).
	Port          *int    `json:"port,omitempty"`
	SocksPort     *int    `json:"socks-port,omitempty"`
	TCPConcurrent *bool   `json:"tcp-concurrent,omitempty"`
	InterfaceName *string `json:"interface-name,omitempty"`
}

// Configs is the subset of GET /configs we read.
type Configs struct {
	Mode      string `json:"mode"`
	MixedPort int    `json:"mixed-port"`
	AllowLan  bool   `json:"allow-lan"`
	LogLevel  string `json:"log-level"`
	Tun       struct {
		Enable bool   `json:"enable"`
		Stack  string `json:"stack"`
	} `json:"tun"`
}

// Rule is an entry of GET /rules.
type Rule struct {
	Index   int    `json:"index"`
	Type    string `json:"type"`
	Payload string `json:"payload"`
	Proxy   string `json:"proxy"`
	Size    int    `json:"size"`
	Extra   *struct {
		Disabled  bool      `json:"disabled"`
		HitCount  uint64    `json:"hitCount"`
		HitAt     time.Time `json:"hitAt"`
		MissCount uint64    `json:"missCount"`
		MissAt    time.Time `json:"missAt"`
	} `json:"extra,omitempty"`
}

// RuleProvider is an entry of GET /providers/rules.
type RuleProvider struct {
	Name        string    `json:"name"`
	Behavior    string    `json:"behavior"`
	Format      string    `json:"format"`
	RuleCount   int       `json:"ruleCount"`
	Type        string    `json:"type"`
	VehicleType string    `json:"vehicleType"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// Metadata is a connection's metadata.
type Metadata struct {
	Network           string `json:"network"`
	Type              string `json:"type"`
	SourceIP          string `json:"sourceIP"`
	DestinationIP     string `json:"destinationIP"`
	SourcePort        string `json:"sourcePort"`
	DestinationPort   string `json:"destinationPort"`
	Host              string `json:"host"`
	SniffHost         string `json:"sniffHost"`
	Process           string `json:"process"`
	ProcessPath       string `json:"processPath"`
	RemoteDestination string `json:"remoteDestination"`
}

// Connection is an entry of the connections snapshot.
type Connection struct {
	ID          string    `json:"id"`
	Metadata    Metadata  `json:"metadata"`
	Upload      int64     `json:"upload"`
	Download    int64     `json:"download"`
	Start       time.Time `json:"start"`
	Chains      []string  `json:"chains"` // innermost (node) first, as Mihomo reports
	Rule        string    `json:"rule"`
	RulePayload string    `json:"rulePayload"`
}

// Connections is GET /connections and each WebSocket frame.
type Connections struct {
	DownloadTotal int64        `json:"downloadTotal"`
	UploadTotal   int64        `json:"uploadTotal"`
	Connections   []Connection `json:"connections"`
	Memory        int64        `json:"memory"`
}

// Traffic is one /traffic frame (bytes per second).
type Traffic struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

// Memory is one /memory frame.
type Memory struct {
	InUse   int64 `json:"inuse"`
	OSLimit int64 `json:"oslimit"`
}

// Log is one /logs frame.
type Log struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
}

// DNSAnswer is one answer of /dns/query.
type DNSAnswer struct {
	Name string `json:"name"`
	Type int    `json:"type"`
	TTL  int    `json:"TTL"`
	Data string `json:"data"`
}

// DNSResult is GET /dns/query.
type DNSResult struct {
	Status int         `json:"Status"`
	Answer []DNSAnswer `json:"Answer"`
}

// ---- REST ----

func (c *Client) Version(ctx context.Context) (string, error)  { return c.c.version(ctx) }
func (c *Client) Configs(ctx context.Context) (Configs, error) { return c.c.configs(ctx) }
func (c *Client) PatchConfigs(ctx context.Context, p ConfigPatch) error {
	return c.c.patchConfigs(ctx, p)
}

// ReloadConfig does PUT /configs?force=true with {"path": path}.
func (c *Client) ReloadConfig(ctx context.Context, path string) error { return c.c.reload(ctx, path) }

// UpdateGeo does POST /configs/geo.
func (c *Client) UpdateGeo(ctx context.Context) error { return c.c.updateGeo(ctx) }

func (c *Client) Proxies(ctx context.Context) (map[string]Proxy, error) { return c.c.proxies(ctx) }
func (c *Client) SelectProxy(ctx context.Context, group, name string) error {
	return c.c.selectProxy(ctx, group, name)
}

// ProxyDelay does GET /proxies/{name}/delay; returns ms.
func (c *Client) ProxyDelay(ctx context.Context, name, url string, timeout time.Duration) (int, error) {
	return c.c.proxyDelay(ctx, name, url, timeout)
}

// GroupDelay does GET /group/{name}/delay; returns ms by member name
// (members that failed are absent).
func (c *Client) GroupDelay(ctx context.Context, group, url string, timeout time.Duration) (map[string]int, error) {
	return c.c.groupDelay(ctx, group, url, timeout)
}

func (c *Client) Rules(ctx context.Context) ([]Rule, error) { return c.c.rules(ctx) }

// DisableRules does PATCH /rules/disable with {index: disabled}.
func (c *Client) DisableRules(ctx context.Context, m map[int]bool) error {
	return c.c.disableRules(ctx, m)
}

func (c *Client) RuleProviders(ctx context.Context) (map[string]RuleProvider, error) {
	return c.c.ruleProviders(ctx)
}
func (c *Client) UpdateRuleProvider(ctx context.Context, name string) error {
	return c.c.updateRuleProvider(ctx, name)
}

func (c *Client) Connections(ctx context.Context) (Connections, error) { return c.c.connections(ctx) }
func (c *Client) CloseConnection(ctx context.Context, id string) error { return c.c.closeConn(ctx, id) }
func (c *Client) CloseAllConnections(ctx context.Context) error        { return c.c.closeAll(ctx) }

func (c *Client) DNSQuery(ctx context.Context, name, qtype string) (DNSResult, error) {
	return c.c.dnsQuery(ctx, name, qtype)
}
func (c *Client) FlushDNSCache(ctx context.Context) error { return c.c.flush(ctx, "/cache/dns/flush") }
func (c *Client) FlushFakeIPCache(ctx context.Context) error {
	return c.c.flush(ctx, "/cache/fakeip/flush")
}

// ---- streams ----
//
// Each stream connects over WebSocket, calls fn for every frame, reconnects
// with backoff (0.5 s → 5 s) while ctx is alive, and returns ctx.Err() when
// ctx is done.

func (c *Client) StreamTraffic(ctx context.Context, fn func(Traffic)) error {
	return c.c.streamTraffic(ctx, fn)
}
func (c *Client) StreamMemory(ctx context.Context, fn func(Memory)) error {
	return c.c.streamMemory(ctx, fn)
}

// StreamLogs streams logs at level ("debug", "info", "warning", "error").
func (c *Client) StreamLogs(ctx context.Context, level string, fn func(Log)) error {
	return c.c.streamLogs(ctx, level, fn)
}

// StreamConnections streams snapshots every interval (Mihomo's ?interval= in ms).
func (c *Client) StreamConnections(ctx context.Context, interval time.Duration, fn func(Connections)) error {
	return c.c.streamConnections(ctx, interval, fn)
}
