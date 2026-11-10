package netinfo

import (
	"net"
	"reflect"
	"testing"
)

const routeSample = `   route to: default
destination: default
       mask: default
    gateway: 192.168.0.1
  interface: en0
      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING,GLOBAL>
 recvpipe  sendpipe  ssthresh  rtt,msec    rttvar  hopcount      mtu     expire
       0         0         0         0         0         0      1500         0 
`

const routeTunSample = `   route to: default
destination: default
       mask: default
    gateway: link#25
  interface: utun4
      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING,GLOBAL>
`

func TestParseRouteDefault(t *testing.T) {
	if got := parseRouteDefault(routeSample); got != (defaultRoute{Interface: "en0", Gateway: "192.168.0.1"}) {
		t.Fatalf("got %+v", got)
	}
	if got := parseRouteDefault(routeTunSample); got != (defaultRoute{Interface: "utun4"}) {
		t.Fatalf("link# gateway should be dropped, got %+v", got)
	}
	if got := parseRouteDefault("route: writing to routing socket: not in table\n"); got != (defaultRoute{}) {
		t.Fatalf("no route should give zero value, got %+v", got)
	}
}

const netstatSample = `Routing tables

Internet:
Destination        Gateway            Flags               Netif Expire
default            utun4              UGScg               utun4       
default            192.168.0.1        UGScIg                en0       
10.0.0.0/8         utun4              USc                 utun4       
127                127.0.0.1          UCS                   lo0       
`

func TestParseNetstatDefaults(t *testing.T) {
	got := parseNetstatDefaults(netstatSample)
	want := []defaultRoute{{Interface: "utun4"}, {Interface: "en0", Gateway: "192.168.0.1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

const hardwarePortsSample = `
Hardware Port: Ethernet Adapter (en4)
Device: en4
Ethernet Address: de:84:63:73:8e:b3

Hardware Port: Thunderbolt Bridge
Device: bridge0
Ethernet Address: 36:99:d6:95:cf:40

Hardware Port: Wi-Fi
Device: en0
Ethernet Address: bc:d0:74:26:da:e3

VLAN Configurations
===================
`

func TestParseHardwarePorts(t *testing.T) {
	got := parseHardwarePorts(hardwarePortsSample)
	want := map[string]string{"en4": "Ethernet Adapter (en4)", "bridge0": "Thunderbolt Bridge", "en0": "Wi-Fi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if !isWiFiPort(got["en0"]) || isWiFiPort(got["en4"]) {
		t.Fatal("isWiFiPort misclassified")
	}
}

const scutilDNSSample = `DNS configuration

resolver #1
  nameserver[0] : 223.5.5.5
  nameserver[1] : 223.5.5.5
  nameserver[2] : fe80::1%en0
  flags    : Request A records
  reach    : 0x00000002 (Reachable)

resolver #2
  domain   : local
  options  : mdns
  timeout  : 5

resolver #3
  domain   : corp.example
  nameserver[0] : 10.1.1.1

DNS configuration (for scoped queries)

resolver #1
  nameserver[0] : 8.8.8.8
  if_index : 15 (en0)
`

const scutilDNSNoPrimary = `DNS configuration

resolver #1
  flags    : Request A records
  reach    : 0x00000000 (Not Reachable)

resolver #2
  domain   : local
  options  : mdns

resolver #3
  domain   : corp.example
  nameserver[0] : 10.1.1.1
  nameserver[1] : 10.1.1.2

resolver #4
  domain   : other
  nameserver[0] : 10.9.9.9
`

func TestParseScutilDNS(t *testing.T) {
	if got, want := parseScutilDNS(scutilDNSSample), []string{"223.5.5.5", "fe80::1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got, want := parseScutilDNS(scutilDNSNoPrimary), []string{"10.1.1.1", "10.1.1.2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback: got %v, want %v", got, want)
	}
	if got := parseScutilDNS(""); got != nil {
		t.Fatalf("empty: got %v", got)
	}
}

const ipconfigRedacted = `<dictionary> {
  BSSID : <redacted>
  InterfaceType : WiFi
  LinkStatusActive : TRUE
  SSID : <redacted>
  Security : WPA3_SAE
}
`

const ipconfigVisible = `<dictionary> {
  BSSID : aa:bb:cc:dd:ee:ff
  InterfaceType : WiFi
  SSID : Home Net 5G
  Security : WPA2_PSK
}
`

const ipconfigEthernet = `<dictionary> {
  InterfaceType : Ethernet
  LinkStatusActive : TRUE
}
`

func TestParseIPConfigSummary(t *testing.T) {
	if got := parseIPConfigSummary(ipconfigRedacted); got != (ipconfigSummary{WiFi: true}) {
		t.Fatalf("redacted: got %+v", got)
	}
	if got := parseIPConfigSummary(ipconfigVisible); got != (ipconfigSummary{SSID: "Home Net 5G", WiFi: true}) {
		t.Fatalf("visible: got %+v", got)
	}
	if got := parseIPConfigSummary(ipconfigEthernet); got != (ipconfigSummary{}) {
		t.Fatalf("ethernet: got %+v", got)
	}
}

func TestParseAirportNetwork(t *testing.T) {
	cases := map[string]string{
		"Current Wi-Fi Network: Home Net 5G\n":                     "Home Net 5G",
		"Current Wi-Fi Network: <redacted>\n":                      "",
		"You are not associated with an AirPort network.\n":        "",
		"en4 is not a Wi-Fi interface.\n** Error: Error obtaining": "",
		"": "",
	}
	for in, want := range cases {
		if got := parseAirportNetwork(in); got != want {
			t.Errorf("parseAirportNetwork(%q) = %q, want %q", in, got, want)
		}
	}
}

const netshSample = `
There are 2 interfaces on the system:

    Name                   : Wi-Fi 2
    Description            : Intel(R) Wi-Fi 6 AX201 160MHz
    GUID                   : 0b4d...
    Physical address       : 00:11:22:33:44:55
    State                  : disconnected
    Radio status           : Hardware On
                             Software On

    Name                   : Wi-Fi
    Description            : Intel(R) Wi-Fi 6 AX201 160MHz
    GUID                   : 1c5e...
    Physical address       : 00:11:22:33:44:66
    Interface type         : Primary
    State                  : connected
    SSID                   : Office: Guest
    BSSID                  : aa:bb:cc:dd:ee:ff
    Network type           : Infrastructure
    Radio type             : 802.11ax
    Authentication         : WPA2-Personal
    Signal                 : 90%

Hosted network status  : Not started
`

func TestParseNetshWlan(t *testing.T) {
	if got := parseNetshWlan(netshSample, "Wi-Fi"); got != "Office: Guest" {
		t.Fatalf("named: got %q", got)
	}
	if got := parseNetshWlan(netshSample, ""); got != "Office: Guest" {
		t.Fatalf("first connected: got %q", got)
	}
	if got := parseNetshWlan(netshSample, "Wi-Fi 2"); got != "" {
		t.Fatalf("disconnected adapter should give empty SSID, got %q", got)
	}
	if got := parseNetshWlan("The Wireless AutoConfig Service (wlansvc) is not running.\n", "Wi-Fi"); got != "" {
		t.Fatalf("service down: got %q", got)
	}
}

func TestIsPublicIPv6(t *testing.T) {
	cases := map[string]bool{
		"2001:db8::1":        true,
		"2409:8a00:1::2":     true,
		"3fff::1":            true,
		"fd00::1":            false,
		"fe80::1":            false,
		"::1":                false,
		"::ffff:192.168.1.1": false,
		"192.168.1.1":        false,
		"4000::1":            false,
	}
	for s, want := range cases {
		if got := isPublicIPv6(net.ParseIP(s)); got != want {
			t.Errorf("isPublicIPv6(%s) = %v, want %v", s, got, want)
		}
	}
	if isPublicIPv6(nil) {
		t.Error("nil should be false")
	}
}

func TestIsTunnelName(t *testing.T) {
	for _, n := range []string{"utun4", "tun0", "tap1", "ppp0", "bridge100", "awdl0"} {
		if !isTunnelName(n) {
			t.Errorf("%s should be a tunnel", n)
		}
	}
	for _, n := range []string{"en0", "en4", "eth0", "wlan0", "Wi-Fi"} {
		if isTunnelName(n) {
			t.Errorf("%s should not be a tunnel", n)
		}
	}
}

func TestDedup(t *testing.T) {
	got := dedup([]string{"a", " ", "b", "a", "", "b "})
	if want := []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
