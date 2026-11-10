package sysproxy

import (
	"reflect"
	"testing"
)

func TestFormatProxyServer(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"127.0.0.1", 7890, "http=127.0.0.1:7890;https=127.0.0.1:7890;socks=127.0.0.1:7890"},
		{"::1", 7890, "http=[::1]:7890;https=[::1]:7890;socks=[::1]:7890"},
	}
	for _, c := range cases {
		if got := formatProxyServer(c.host, c.port); got != c.want {
			t.Errorf("formatProxyServer(%q,%d) = %q, want %q", c.host, c.port, got, c.want)
		}
	}
}

func TestParseProxyServer(t *testing.T) {
	cases := []struct {
		in   string
		host string
		port int
	}{
		{"http=127.0.0.1:7890;https=127.0.0.1:7890;socks=127.0.0.1:7890", "127.0.0.1", 7890},
		{"127.0.0.1:7890", "127.0.0.1", 7890},
		{"socks=127.0.0.1:7891", "127.0.0.1", 7891},
		{"https=10.0.0.1:3128;http=10.0.0.2:8080", "10.0.0.2", 8080},
		{"http=[::1]:7890", "::1", 7890},
		{"proxy.corp", "proxy.corp", 0},
		{"", "", 0},
		{" ; ", "", 0},
	}
	for _, c := range cases {
		h, p := parseProxyServer(c.in)
		if h != c.host || p != c.port {
			t.Errorf("parseProxyServer(%q) = %q,%d; want %q,%d", c.in, h, p, c.host, c.port)
		}
	}
}

func TestProxyServerRoundTrip(t *testing.T) {
	h, p := parseProxyServer(formatProxyServer("192.168.1.10", 1080))
	if h != "192.168.1.10" || p != 1080 {
		t.Fatalf("round trip gave %q %d", h, p)
	}
}

func TestFormatProxyOverride(t *testing.T) {
	got := formatProxyOverride(DefaultBypass)
	want := "localhost;127.0.0.1;::1;192.168.0.0/16;10.0.0.0/8;172.16.0.0/12;*.local;*.lan;<local>"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := formatProxyOverride(nil); got != "<local>" {
		t.Fatalf("empty bypass should still contain <local>, got %q", got)
	}
	if got := formatProxyOverride([]string{"a", "<LOCAL>", "a"}); got != "a;<LOCAL>" {
		t.Fatalf("dedup/case handling wrong: %q", got)
	}
}

func TestParseProxyOverride(t *testing.T) {
	got := parseProxyOverride("localhost;127.0.0.1; *.local ;;<local>")
	want := []string{"localhost", "127.0.0.1", "*.local", "<local>"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if parseProxyOverride("") != nil {
		t.Fatal("empty override should give nil")
	}
}
