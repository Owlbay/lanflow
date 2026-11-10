package atrust

import (
	"net/netip"
	"os"
	"reflect"
	"strconv"
	"testing"
)

func TestNetstatCIDR(t *testing.T) {
	cases := []struct{ dst, flags, want string }{
		{"10.1", "UGSc", "10.1.0.0/16"},
		{"2.0.0/24", "UGSc", "2.0.0.0/24"},
		{"172.16/12", "UGSc", "172.16.0.0/12"},
		{"2.0.0.5", "UH", "2.0.0.5/32"},
		{"10", "UGSc", "10.0.0.0/8"},
		{"192.168.10.0/23", "UGSc", "192.168.10.0/23"},
	}
	for _, c := range cases {
		p, ok := netstatCIDR(c.dst, c.flags)
		if !ok || p.String() != c.want {
			t.Errorf("%s %s = %v %v, want %s", c.dst, c.flags, p, ok, c.want)
		}
	}
	for _, bad := range []string{"fe80::%utun0", "x.y", "1.2.3.4.5"} {
		if _, ok := netstatCIDR(bad, "U"); ok {
			t.Errorf("%s accepted", bad)
		}
	}
}

const netstatOut = `Routing tables

Internet:
Destination        Gateway            Flags               Netif Expire
default            192.168.1.1        UGScg                 en0
1                  198.18.0.1         UGSc                utun4
2.0.0/24           2.0.0.1            UGSc                utun6
2.0.0.1            2.0.0.1            UH                  utun6
10.20/16           2.0.0.1            UGSc                utun6
10.20.3/24         2.0.0.1            UGSc                utun6
127                127.0.0.1          UCS                   lo0
169.254            link#5             UCS                   en0      !
198.18/16          198.18.0.1         UGSc                utun4
224.0.0/4          link#22            UmCSI               utun6
default            link#22            UCSIg               utun6
100.64/10          link#9             UCSI                utun3
0/1                198.18.0.1         UGSc             utun1500
198.18.0.1         198.18.0.1         UH               utun1500
`

func TestParseRoutes(t *testing.T) {
	own := func(i string) bool { return i == "utun4" }
	rs := parseRoutes(netstatOut, own)
	var got []string
	for _, r := range rs {
		got = append(got, r.prefix.String()+"@"+r.iface)
	}
	want := []string{"2.0.0.0/24@utun6", "2.0.0.1/32@utun6", "10.20.0.0/16@utun6", "10.20.3.0/24@utun6"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routes = %v, want %v", got, want)
	}
}

const scutilOut = `DNS configuration

resolver #1
  search domain[0] : corp.example.com
  nameserver[0] : 192.168.1.1
  if_index : 6 (en0)
  flags    : Request A records
  reach    : 0x00020002 (Reachable,Directly Reachable Address)

resolver #2
  domain   : corp.example.com
  nameserver[0] : 2.0.0.53
  nameserver[1] : fe80::1
  if_index : 22 (utun6)
  flags    : Supplemental, Request A records
  order    : 102000

resolver #3
  domain   : Intra.Example.NET.
  nameserver[0] : 2.0.0.53
  if_index : 22 (utun6)

resolver #4
  domain   : 0.0.2.in-addr.arpa
  nameserver[0] : 2.0.0.53
  if_index : 22 (utun6)

resolver #5
  domain   : local
  options  : mdns

resolver #6
  domain   : fake.lanflow
  nameserver[0] : 198.18.0.2
  if_index : 21 (utun4)

DNS configuration (for scoped queries)

resolver #1
  domain   : scoped.example.com
  nameserver[0] : 9.9.9.9
  if_index : 22 (utun6)
`

func TestBuild(t *testing.T) {
	own := func(i string) bool { return i == "utun4" }
	info := build([]string{"aTrustAgent"}, parseRoutes(netstatOut, own), parseResolvers(scutilOut), own)
	want := Info{
		Processes: []string{"aTrustAgent"},
		Ifaces:    []string{"utun6"},
		CIDRs:     []string{"2.0.0.0/24", "10.20.0.0/16"},
		Domains:   []string{"corp.example.com", "intra.example.net"},
		DNS:       []string{"2.0.0.53"},
	}
	if !reflect.DeepEqual(info, want) {
		t.Fatalf("info =\n%+v\nwant\n%+v", info, want)
	}
	if !info.Running() || info.Key() == (Info{}).Key() {
		t.Fatal("running/key")
	}
}

func TestMatchProcesses(t *testing.T) {
	got := matchProcesses([]string{
		"/Applications/aTrust.app/Contents/MacOS/aTrust",
		"/Library/Application Support/Sangfor/aTrustAgent",
		"aTrustAgent", "/usr/sbin/mDNSResponder", "SangforServiceClient", "",
	})
	want := []string{"SangforServiceClient", "aTrust", "aTrustAgent"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	names := tasklistNames("\"aTrustTray.exe\",\"1234\",\"Console\",\"1\",\"10,000 K\"\r\n\"explorer.exe\",\"1\",\"Console\",\"1\",\"1 K\"\r\n\"aTrust.test.exe\",\"77\",\"Console\",\"1\",\"1 K\"\r\n", 77)
	if !reflect.DeepEqual(matchProcesses(names), []string{"aTrustTray.exe"}) {
		t.Fatalf("tasklist: %v", names)
	}
}

func TestWithoutSelf(t *testing.T) {
	self := strconv.Itoa(os.Getpid())
	got := withoutSelf([]string{"  1 /sbin/launchd", self + " /tmp/atrust.test", " 88 /Applications/aTrust.app/Contents/MacOS/aTrust", ""})
	if !reflect.DeepEqual(matchProcesses(got), []string{"aTrust"}) {
		t.Fatalf("got %v", got)
	}
}

func TestParseGateways(t *testing.T) {
	out := `Active Internet connections (including servers)
Proto Recv-Q Send-Q  Local Address          Foreign Address        (state)      rxbytes txbytes rhiwat shiwat process:pid state options
tcp4       0      0  192.168.0.7.52964      58.246.145.226.441     ESTABLISHED  0 0 131072 131072 aTrustAgent:812 00184 0
udp4       0      0  192.168.0.7.61000      116.246.24.6.10445                  0 0 131072 131072 aTrustAgent:812 00184 0
tcp4       0      0  2.0.0.5.50000          2.0.0.9.443            ESTABLISHED  0 0 131072 131072 aTrustTray:900 00184 0
tcp4       0      0  127.0.0.1.50001        127.0.0.1.7000         ESTABLISHED  0 0 131072 131072 aTrustAgent:812 00184 0
tcp4       0      0  198.18.0.1.50002       198.18.3.4.443         ESTABLISHED  0 0 131072 131072 aTrustAgent:812 00184 0
tcp4       0      0  192.168.0.7.50003      203.0.113.1.443        ESTABLISHED  0 0 131072 131072 Safari:400 00184 0
tcp6       0      0  ::1.50004              ::1.443                ESTABLISHED  0 0 131072 131072 aTrustAgent:812 00184 0
`
	got := parseGateways(out, []netip.Prefix{netip.MustParsePrefix("2.0.0.0/24")})
	if !reflect.DeepEqual(got, []string{"58.246.145.226", "116.246.24.6"}) {
		t.Fatalf("gateways = %v", got)
	}
}
