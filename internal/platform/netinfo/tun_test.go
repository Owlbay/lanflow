package netinfo

import (
	"context"
	"testing"
)

func TestParseDarwinTUNRoute(t *testing.T) {
	clash := `Routing tables

Internet:
Destination        Gateway            Flags               Netif Expire
default            192.168.0.1        UGScg                 en0
default            link#22            UCSIg               utun4
1                  198.18.0.1         UGSc             utun1500
2/7                198.18.0.1         UGSc             utun1500
128.0/1            198.18.0.1         UGSc             utun1500
`
	if name, ok := parseDarwinTUNRoute(clash); !ok || name != "utun1500" {
		t.Fatalf("clash TUN: %q %v", name, ok)
	}
	plain := `Destination        Gateway            Flags               Netif Expire
default            192.168.0.1        UGScg                 en0
default            link#22            UCSIg               utun4
100.64/10          link#22            UCS                 utun4
`
	if name, ok := parseDarwinTUNRoute(plain); ok {
		t.Fatalf("no TUN expected, got %q (scoped default and Tailscale routes must not count)", name)
	}
}

func TestParseWindowsTUNRoute(t *testing.T) {
	out := `===========================================================================
Active Routes:
Network Destination        Netmask          Gateway       Interface  Metric
          0.0.0.0          0.0.0.0      192.168.1.1    192.168.1.23     25
          0.0.0.0        128.0.0.0         On-link       198.18.0.1      0
        128.0.0.0        128.0.0.0         On-link       198.18.0.1      0
`
	if addr, ok := parseWindowsTUNRoute(out); !ok || addr != "198.18.0.1" {
		t.Fatalf("got %q %v", addr, ok)
	}
	if _, ok := parseWindowsTUNRoute("          0.0.0.0          0.0.0.0      192.168.1.1    192.168.1.23     25\n"); ok {
		t.Fatal("plain default must not count")
	}
}

// Smoke test on the host: must return quickly; logs what it sees.
func TestOtherTUNSmoke(t *testing.T) {
	name, ok := OtherTUN(context.Background())
	t.Logf("other TUN on this host: %q %v", name, ok)
}
