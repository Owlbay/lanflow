package sysproxy

import (
	"context"
	"reflect"
	"testing"
)

func TestParseGetProxy(t *testing.T) {
	got := parseGetProxy("Enabled: Yes\nServer: 10.0.0.2\nPort: 3128\nAuthenticated Proxy Enabled: 0\n")
	if got != (darwinProxy{On: true, Server: "10.0.0.2", Port: 3128}) {
		t.Fatalf("got %+v", got)
	}
	if got := parseGetProxy("Enabled: No\nServer: \nPort: 0\n"); got != (darwinProxy{}) {
		t.Fatalf("off: got %+v", got)
	}
}

func TestParseBypassDomains(t *testing.T) {
	if got := parseBypassDomains("There aren't any bypass domains set on Wi-Fi.\n"); got != nil {
		t.Fatalf("empty: %q", got)
	}
	if got := parseBypassDomains("*.local\n169.254/16\n"); !reflect.DeepEqual(got, []string{"*.local", "169.254/16"}) {
		t.Fatalf("got %q", got)
	}
}

// Another app's manual proxy on Wi-Fi must come back exactly, off services
// stay off, and a service added since the snapshot is cleared.
func TestDarwinSnapshotRestoreRoundTrip(t *testing.T) {
	f := newFake()
	f.outputs["networksetup -listallnetworkservices"] = "An asterisk (*) denotes that a network service is disabled.\nWi-Fi\nUSB LAN\n"
	f.outputs["networksetup -getwebproxy Wi-Fi"] = "Enabled: Yes\nServer: 127.0.0.1\nPort: 7897\n"
	f.outputs["networksetup -getsecurewebproxy Wi-Fi"] = "Enabled: Yes\nServer: 127.0.0.1\nPort: 7897\n"
	f.outputs["networksetup -getsocksfirewallproxy Wi-Fi"] = "Enabled: No\nServer: \nPort: 0\n"
	f.outputs["networksetup -getproxybypassdomains Wi-Fi"] = "*.local\nlocalhost\n"
	f.outputs["networksetup -getwebproxy USB LAN"] = "Enabled: No\nServer: \nPort: 0\n"
	f.outputs["networksetup -getsecurewebproxy USB LAN"] = "Enabled: No\nServer: \nPort: 0\n"
	f.outputs["networksetup -getsocksfirewallproxy USB LAN"] = "Enabled: No\nServer: \nPort: 0\n"
	f.outputs["networksetup -getproxybypassdomains USB LAN"] = "There aren't any bypass domains set on USB LAN.\n"

	saved, err := darwinSnapshot(context.Background(), f.run)
	if err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	f.outputs["networksetup -listallnetworkservices"] = "Wi-Fi\nUSB LAN\nThunderbolt Bridge\n"
	if err := darwinRestore(context.Background(), f.run, saved); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"networksetup -listallnetworkservices",
		"networksetup -setwebproxy Wi-Fi 127.0.0.1 7897",
		"networksetup -setwebproxystate Wi-Fi on",
		"networksetup -setsecurewebproxy Wi-Fi 127.0.0.1 7897",
		"networksetup -setsecurewebproxystate Wi-Fi on",
		"networksetup -setsocksfirewallproxystate Wi-Fi off",
		"networksetup -setproxybypassdomains Wi-Fi *.local localhost",
		"networksetup -setautoproxystate Wi-Fi off",
		"networksetup -setwebproxystate USB LAN off",
		"networksetup -setsecurewebproxystate USB LAN off",
		"networksetup -setsocksfirewallproxystate USB LAN off",
		"networksetup -setproxybypassdomains USB LAN Empty",
		"networksetup -setautoproxystate USB LAN off",
		"networksetup -setwebproxystate Thunderbolt Bridge off",
		"networksetup -setsecurewebproxystate Thunderbolt Bridge off",
		"networksetup -setsocksfirewallproxystate Thunderbolt Bridge off",
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("restore commands:\n got %q\nwant %q", f.calls, want)
	}
}

func TestDarwinRestoreRejectsForeignSnapshot(t *testing.T) {
	if err := darwinRestore(context.Background(), newFake().run, Saved(`{"os":"windows"}`)); err == nil {
		t.Fatal("expected an error")
	}
}
