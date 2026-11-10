package netinfo

import "testing"

func TestParseDarwinV6Routes(t *testing.T) {
	// A LAN with IPv6 (here a ULA with NAT66) and no TUN: the default
	// route is on en0, nothing covers IPv6 through a utun.
	plain := `Routing tables

Internet6:
Destination                             Gateway                                 Flags               Netif Expire
default                                 fe80::21c:42ff:fe00:18%en0              UGcg                  en0
default                                 fe80::%utun0                            UGcIg               utun0
::1                                     ::1                                     UHL                   lo0
fd07:b51a:cc66::/64                     link#14                                 UC                    en0
fe80::%en0/64                           link#14                                 UCI                   en0
`
	if r := parseDarwinV6Routes(plain); r.Interface != "en0" || r.ViaTUN {
		t.Fatalf("plain = %+v, want en0 without TUN", r)
	}
	// Mihomo's TUN with an inet6 address: eight split routes via utun1500
	// while en0 keeps its default.
	mihomo := plain + `100::/8                                 fdfe:dcba:9876::1                       UGSc             utun1500
200::/7                                 fdfe:dcba:9876::1                       UGSc             utun1500
2000::/3                                fdfe:dcba:9876::1                       UGSc             utun1500
8000::/1                                fdfe:dcba:9876::1                       UGSc             utun1500
fdfe:dcba:9876::/126                    fe80::1459:c4f3:cbf4:b80d%utun1500      Uc               utun1500
`
	if r := parseDarwinV6Routes(mihomo); r.Interface != "en0" || !r.ViaTUN {
		t.Fatalf("mihomo = %+v, want en0 and via TUN", r)
	}
	// The ::/1 + 8000::/1 pair other clients use counts too; a utun's own
	// scoped default (Tailscale, VPNs) does not.
	pair := `default                                 fe80::%utun4                            UGcIg               utun4
::/1                                    link#30                                 UCS              utun1800
8000::/1                                link#30                                 UCS              utun1800
`
	if r := parseDarwinV6Routes(pair); r.Interface != "" || !r.ViaTUN {
		t.Fatalf("pair = %+v, want via TUN and no physical default", r)
	}
	if r := parseDarwinV6Routes("default   fe80::%utun4   UGcIg   utun4\n"); r != (V6Route{}) {
		t.Fatalf("scoped default = %+v, want nothing", r)
	}
}

func TestParseWindowsV6Routes(t *testing.T) {
	out := `IPv6 Route Table
===========================================================================
Active Routes:
 If Metric Network Destination      Gateway
 12    281 ::/0                     fe80::1
 24      0 ::/0                     On-link
  1    331 ::1/128                  On-link
 12    281 fe80::/64                On-link
===========================================================================
`
	if r := parseWindowsV6Routes(out); r.Interface != "if#12" || !r.ViaTUN {
		t.Fatalf("windows = %+v", r)
	}
	noTUN := ` If Metric Network Destination      Gateway
 12    281 ::/0                     fe80::1
`
	if r := parseWindowsV6Routes(noTUN); r.Interface != "if#12" || r.ViaTUN {
		t.Fatalf("windows without TUN = %+v", r)
	}
}
