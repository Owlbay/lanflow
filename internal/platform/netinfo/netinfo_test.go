package netinfo

import (
	"context"
	"net"
	"testing"
	"time"
)

// TestGetSmoke runs the real (read-only) collection on the host and checks
// the 3 s contract. Values depend on the machine, so only shape is checked.
func TestGetSmoke(t *testing.T) {
	start := time.Now()
	info, err := Get(context.Background())
	elapsed := time.Since(start)
	t.Logf("netinfo in %s: %+v (err=%v)", elapsed, info, err)
	if elapsed >= 3*time.Second {
		t.Fatalf("Get took %s, must finish under 3 s", elapsed)
	}
	if info.LANIP != "" && net.ParseIP(info.LANIP) == nil {
		t.Errorf("LANIP %q is not an IP", info.LANIP)
	}
	if info.Gateway != "" && net.ParseIP(info.Gateway) == nil {
		t.Errorf("Gateway %q is not an IP", info.Gateway)
	}
	for _, d := range info.DNS {
		if net.ParseIP(d) == nil {
			t.Errorf("DNS %q is not an IP", d)
		}
	}
	if info.IPv6 != "" && !isPublicIPv6(net.ParseIP(info.IPv6)) {
		t.Errorf("IPv6 %q is not a public address", info.IPv6)
	}
}

// TestGetHonoursDeadline makes sure an already-short caller deadline is
// respected and Get still returns promptly without panicking.
func TestGetHonoursDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _ = Get(ctx)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Get with a 50 ms deadline took %s", d)
	}
}

func TestLANAddress(t *testing.T) {
	addr := LANAddress()
	t.Logf("LANAddress = %q", addr)
	if addr == "" {
		t.Skip("host has no LAN address")
	}
	ip := net.ParseIP(addr)
	if ip == nil || ip.To4() == nil {
		t.Fatalf("LANAddress %q is not an IPv4", addr)
	}
}
