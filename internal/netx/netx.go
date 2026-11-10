// Package netx provides HTTP clients that never use the system proxy
// (Lanflow sets the system proxy itself, so following it would loop).
//
// The exported API of this package is a stable contract for the rest of
// the app.
package netx

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// UserAgent is the default User-Agent for subscription downloads; it makes
// providers return Clash/Mihomo YAML.
const UserAgent = "clash.meta/mihomo Lanflow/0.1"

func transport(proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	return &http.Transport{
		Proxy:                 proxy,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
}

// Direct returns a client that connects directly, ignoring HTTP_PROXY and
// the system proxy.
func Direct(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: transport(nil)}
}

// ViaPort returns a client whose requests go through the HTTP proxy at
// 127.0.0.1:port (the core's mixed port or a probe listener).
func ViaPort(port int, timeout time.Duration) *http.Client {
	u := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}
	t := transport(http.ProxyURL(u))
	t.DisableKeepAlives = true // each probe must open a fresh connection through the selected node
	return &http.Client{Timeout: timeout, Transport: t}
}
