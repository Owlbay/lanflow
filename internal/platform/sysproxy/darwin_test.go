package sysproxy

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const sampleServices = `An asterisk (*) denotes that a network service is disabled.
USB 10/100/1000 LAN
*Thunderbolt Bridge
Wi-Fi

iPhone USB
`

// fakeExecutor records every command; fail maps "cmd args" prefixes to
// errors so individual services can be made to fail.
type fakeExecutor struct {
	calls   []string
	fail    map[string]error
	outputs map[string]string
}

func (f *fakeExecutor) run(_ context.Context, name string, args ...string) (string, error) {
	line := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, line)
	for prefix, err := range f.fail {
		if strings.HasPrefix(line, prefix) {
			return "", err
		}
	}
	if out, ok := f.outputs[line]; ok {
		return out, nil
	}
	return "", nil
}

func newFake() *fakeExecutor {
	return &fakeExecutor{
		fail: map[string]error{},
		outputs: map[string]string{
			"networksetup -listallnetworkservices": sampleServices,
		},
	}
}

func TestParseNetworkServices(t *testing.T) {
	got := parseNetworkServices(sampleServices)
	want := []string{"USB 10/100/1000 LAN", "Wi-Fi", "iPhone USB"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDarwinSetCommands(t *testing.T) {
	f := newFake()
	err := darwinSet(context.Background(), f.run, "127.0.0.1", 7890, []string{"localhost", "127.0.0.1", "<local>", "*.local", "localhost"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"networksetup -listallnetworkservices",
	}
	for _, svc := range []string{"USB 10/100/1000 LAN", "Wi-Fi", "iPhone USB"} {
		want = append(want,
			"networksetup -setwebproxy "+svc+" 127.0.0.1 7890",
			"networksetup -setwebproxystate "+svc+" on",
			"networksetup -setsecurewebproxy "+svc+" 127.0.0.1 7890",
			"networksetup -setsecurewebproxystate "+svc+" on",
			"networksetup -setsocksfirewallproxy "+svc+" 127.0.0.1 7890",
			"networksetup -setsocksfirewallproxystate "+svc+" on",
			"networksetup -setproxybypassdomains "+svc+" localhost 127.0.0.1 *.local",
		)
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("command sequence mismatch\n got: %s\nwant: %s", strings.Join(f.calls, "\n      "), strings.Join(want, "\n      "))
	}
}

func TestDarwinSetEmptyBypassUsesEmptyKeyword(t *testing.T) {
	cmds := setCommands("Wi-Fi", "127.0.0.1", 7890, []string{"<local>"})
	last := cmds[len(cmds)-1]
	want := command{"-setproxybypassdomains", "Wi-Fi", "Empty"}
	if !reflect.DeepEqual(last, want) {
		t.Fatalf("got %q, want %q", last, want)
	}
}

func TestDarwinSetInvalidAddress(t *testing.T) {
	f := newFake()
	if err := darwinSet(context.Background(), f.run, "", 7890, nil); err == nil {
		t.Fatal("expected error for empty host")
	}
	if err := darwinSet(context.Background(), f.run, "127.0.0.1", 70000, nil); err == nil {
		t.Fatal("expected error for bad port")
	}
	if len(f.calls) != 0 {
		t.Fatalf("no commands should run for invalid input, got %q", f.calls)
	}
}

func TestDarwinClearCommands(t *testing.T) {
	f := newFake()
	if err := darwinClear(context.Background(), f.run); err != nil {
		t.Fatal(err)
	}
	want := []string{"networksetup -listallnetworkservices"}
	for _, svc := range []string{"USB 10/100/1000 LAN", "Wi-Fi", "iPhone USB"} {
		want = append(want,
			"networksetup -setwebproxystate "+svc+" off",
			"networksetup -setsecurewebproxystate "+svc+" off",
			"networksetup -setsocksfirewallproxystate "+svc+" off",
		)
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("command sequence mismatch\n got: %s\nwant: %s", strings.Join(f.calls, "\n      "), strings.Join(want, "\n      "))
	}
	for _, c := range f.calls {
		if strings.Contains(c, "-setwebproxy ") || strings.Contains(c, "-setsecurewebproxy ") || strings.Contains(c, "-setsocksfirewallproxy ") {
			t.Fatalf("Clear must not rewrite host/port: %s", c)
		}
	}
}

func TestDarwinSetAggregatesErrorsAndContinues(t *testing.T) {
	f := newFake()
	f.fail["networksetup -setwebproxystate USB 10/100/1000 LAN"] = errors.New("boom")
	f.fail["networksetup -setsocksfirewallproxy iPhone USB"] = errors.New("nope")
	err := darwinSet(context.Background(), f.run, "127.0.0.1", 7890, DefaultBypass)
	if err == nil {
		t.Fatal("expected aggregated error")
	}
	msg := err.Error()
	for _, want := range []string{"设置系统代理失败", "2/3", "USB 10/100/1000 LAN：boom", "iPhone USB：nope"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q should contain %q", msg, want)
		}
	}
	// Wi-Fi, the healthy service in the middle, must have been configured fully.
	joined := strings.Join(f.calls, "\n")
	if !strings.Contains(joined, "-setproxybypassdomains Wi-Fi ") {
		t.Errorf("Wi-Fi should still be configured after an earlier failure:\n%s", joined)
	}
	// The failing service stops at its first error (no later commands for it).
	if strings.Contains(joined, "-setsecurewebproxy USB 10/100/1000 LAN") {
		t.Errorf("commands after a failure for the same service should be skipped:\n%s", joined)
	}
}

func TestDarwinNoServices(t *testing.T) {
	f := newFake()
	f.outputs["networksetup -listallnetworkservices"] = "An asterisk (*) denotes that a network service is disabled.\n*Wi-Fi\n"
	err := darwinSet(context.Background(), f.run, "127.0.0.1", 7890, nil)
	if err == nil || !strings.Contains(err.Error(), "没有已启用的网络服务") {
		t.Fatalf("got %v", err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("only the listing should run, got %q", f.calls)
	}
}

func TestDarwinListFailure(t *testing.T) {
	f := newFake()
	f.fail["networksetup -listallnetworkservices"] = errors.New("命令 networksetup 超过 5 秒未返回")
	err := darwinClear(context.Background(), f.run)
	if err == nil || !strings.Contains(err.Error(), "无法列出网络服务") || !strings.Contains(err.Error(), "5 秒") {
		t.Fatalf("got %v", err)
	}
}

const scutilEnabled = `<dictionary> {
  ExceptionsList : <array> {
    0 : localhost
    1 : 127.0.0.1
    2 : *.local
  }
  FTPPassive : 1
  HTTPEnable : 1
  HTTPPort : 7890
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7890
  HTTPSProxy : 127.0.0.1
  ProxyAutoConfigEnable : 0
  ProxyAutoDiscoveryEnable : 0
  SOCKSEnable : 1
  SOCKSPort : 7890
  SOCKSProxy : 127.0.0.1
}
`

const scutilDisabled = `<dictionary> {
  ExceptionsList : <array> {
    0 : dns.google
    1 : one.one.one.one
  }
  HTTPEnable : 0
  HTTPSEnable : 0
  ProxyAutoConfigEnable : 0
  ProxyAutoDiscoveryEnable : 0
  SOCKSEnable : 0
}
`

const scutilSocksOnly = `<dictionary> {
  HTTPEnable : 0
  HTTPPort : 8080
  HTTPProxy : 10.0.0.9
  HTTPSEnable : 0
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 192.168.1.2
}
`

func TestParseScutilProxy(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want State
	}{
		{"enabled", scutilEnabled, State{Enabled: true, Host: "127.0.0.1", Port: 7890, Bypass: []string{"localhost", "127.0.0.1", "*.local"}}},
		{"disabled", scutilDisabled, State{Enabled: false, Bypass: []string{"dns.google", "one.one.one.one"}}},
		{"socks only prefers the enabled protocol", scutilSocksOnly, State{Enabled: true, Host: "192.168.1.2", Port: 1080}},
		{"empty", "", State{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseScutilProxy(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestDarwinGet(t *testing.T) {
	f := newFake()
	f.outputs["scutil --proxy"] = scutilEnabled
	st, err := darwinGet(context.Background(), f.run)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || st.Host != "127.0.0.1" || st.Port != 7890 {
		t.Fatalf("got %+v", st)
	}
	if !reflect.DeepEqual(f.calls, []string{"scutil --proxy"}) {
		t.Fatalf("calls %q", f.calls)
	}
}
