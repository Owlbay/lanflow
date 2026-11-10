package sysproxy

import (
	"context"
	"reflect"
	"testing"
)

const scutilPAC = `<dictionary> {
  HTTPEnable : 0
  HTTPSEnable : 0
  ProxyAutoConfigEnable : 1
  ProxyAutoConfigURLString : http://127.0.0.1:11233/proxy.pac
  ProxyAutoDiscoveryEnable : 0
  SOCKSEnable : 0
}
`

func TestParseScutilProxyPAC(t *testing.T) {
	got := parseScutilProxy(scutilPAC)
	if got.Enabled || got.PACURL != "http://127.0.0.1:11233/proxy.pac" {
		t.Fatalf("got %+v", got)
	}
	if off := parseScutilProxy(scutilDisabled); off.PACURL != "" {
		t.Fatalf("PAC off must leave PACURL empty: %+v", off)
	}
}

func TestDarwinSetPACCommands(t *testing.T) {
	f := newFake()
	if err := darwinSetPAC(context.Background(), f.run, "http://127.0.0.1:11233/proxy.pac"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"networksetup -listallnetworkservices",
		"networksetup -setautoproxyurl USB 10/100/1000 LAN http://127.0.0.1:11233/proxy.pac",
		"networksetup -setautoproxystate USB 10/100/1000 LAN on",
		"networksetup -setautoproxyurl Wi-Fi http://127.0.0.1:11233/proxy.pac",
		"networksetup -setautoproxystate Wi-Fi on",
		"networksetup -setautoproxyurl iPhone USB http://127.0.0.1:11233/proxy.pac",
		"networksetup -setautoproxystate iPhone USB on",
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("set PAC:\n got %q\nwant %q", f.calls, want)
	}
	f.calls = nil
	if err := darwinSetPAC(context.Background(), f.run, ""); err != nil {
		t.Fatal(err)
	}
	want = []string{
		"networksetup -listallnetworkservices",
		"networksetup -setautoproxystate USB 10/100/1000 LAN off",
		"networksetup -setautoproxystate Wi-Fi off",
		"networksetup -setautoproxystate iPhone USB off",
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("clear PAC:\n got %q\nwant %q", f.calls, want)
	}
	if err := darwinSetPAC(context.Background(), f.run, "ftp://x"); err == nil {
		t.Fatal("a non-http URL must be refused")
	}
}

func TestParseGetAutoProxy(t *testing.T) {
	a := parseGetAutoProxy("URL: http://pac.corp/proxy.pac\nEnabled: Yes\n")
	if !a.On || a.URL != "http://pac.corp/proxy.pac" {
		t.Fatalf("got %+v", a)
	}
	a = parseGetAutoProxy("URL: (null)\nEnabled: No\n")
	if a.On || a.URL != "" {
		t.Fatalf("got %+v", a)
	}
}

// TestDarwinSnapshotRestoresForeignPAC: a PAC another program configured
// is put back, URL and state.
func TestDarwinSnapshotRestoresForeignPAC(t *testing.T) {
	f := newFake()
	f.outputs["networksetup -listallnetworkservices"] = "Wi-Fi\n"
	f.outputs["networksetup -getwebproxy Wi-Fi"] = "Enabled: No\nServer: \nPort: 0\n"
	f.outputs["networksetup -getsecurewebproxy Wi-Fi"] = "Enabled: No\nServer: \nPort: 0\n"
	f.outputs["networksetup -getsocksfirewallproxy Wi-Fi"] = "Enabled: No\nServer: \nPort: 0\n"
	f.outputs["networksetup -getproxybypassdomains Wi-Fi"] = "There aren't any bypass domains set on Wi-Fi.\n"
	f.outputs["networksetup -getautoproxyurl Wi-Fi"] = "URL: http://pac.corp/proxy.pac\nEnabled: Yes\n"
	saved, err := darwinSnapshot(context.Background(), f.run)
	if err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	if err := darwinRestore(context.Background(), f.run, saved); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"networksetup -listallnetworkservices",
		"networksetup -setwebproxystate Wi-Fi off",
		"networksetup -setsecurewebproxystate Wi-Fi off",
		"networksetup -setsocksfirewallproxystate Wi-Fi off",
		"networksetup -setproxybypassdomains Wi-Fi Empty",
		"networksetup -setautoproxyurl Wi-Fi http://pac.corp/proxy.pac",
		"networksetup -setautoproxystate Wi-Fi on",
	}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("restore:\n got %q\nwant %q", f.calls, want)
	}
}

// TestDarwinRestoreOldSnapshotLeavesPAC: a snapshot without the auto
// field (older build) restores nothing about PAC.
func TestDarwinRestoreOldSnapshotLeavesPAC(t *testing.T) {
	f := newFake()
	f.outputs["networksetup -listallnetworkservices"] = "Wi-Fi\n"
	saved := Saved(`{"os":"darwin","services":[{"name":"Wi-Fi","web":{"on":false},"secure":{"on":false},"socks":{"on":false}}]}`)
	if err := darwinRestore(context.Background(), f.run, saved); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if reflect.DeepEqual(c[:len("networksetup -setauto")], "networksetup -setauto") {
			t.Fatalf("old snapshot must not touch PAC: %q", f.calls)
		}
	}
}
