package sysdns

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// fakeMac simulates networksetup with per-service DNS state.
type fakeMac struct {
	dns  map[string][]string
	list string
	log  []string
}

func (f *fakeMac) run(_ context.Context, name string, args ...string) (string, error) {
	f.log = append(f.log, name+" "+strings.Join(args, " "))
	if name != networksetup {
		return "", nil
	}
	switch args[0] {
	case "-listallnetworkservices":
		return f.list, nil
	case "-getdnsservers":
		s, ok := f.dns[args[1]]
		if !ok {
			return "", fmt.Errorf("%s is not a recognized network service", args[1])
		}
		if len(s) == 0 {
			return "There aren't any DNS Servers set on " + args[1] + ".\n", nil
		}
		return strings.Join(s, "\n") + "\n", nil
	case "-setdnsservers":
		if args[2] == "Empty" {
			f.dns[args[1]] = nil
		} else {
			f.dns[args[1]] = append([]string(nil), args[2:]...)
		}
		return "", nil
	}
	return "", fmt.Errorf("unexpected %v", args)
}

func newFakeMac() *fakeMac {
	return &fakeMac{
		list: "An asterisk (*) denotes that a network service is disabled.\nWi-Fi\nUSB 10/100/1000 LAN\n*Thunderbolt Bridge\n",
		dns:  map[string][]string{"Wi-Fi": nil, "USB 10/100/1000 LAN": {"192.168.1.1", "8.8.8.8"}, "*Thunderbolt Bridge": nil},
	}
}

func TestDarwinSetAndRestore(t *testing.T) {
	ctx := context.Background()
	f := newFakeMac()
	saved, err := darwinSnapshot(ctx, f.run)
	if err != nil {
		t.Fatal(err)
	}
	if err := darwinSet(ctx, f.run, TUNServers); err != nil {
		t.Fatal(err)
	}
	for _, svc := range []string{"Wi-Fi", "USB 10/100/1000 LAN"} {
		if !slices.Equal(f.dns[svc], TUNServers) {
			t.Fatalf("%s = %v while TUN", svc, f.dns[svc])
		}
	}
	if f.dns["*Thunderbolt Bridge"] != nil {
		t.Fatal("disabled service must not be touched")
	}
	if err := darwinRestore(ctx, f.run, saved, TUNServers); err != nil {
		t.Fatal(err)
	}
	if f.dns["Wi-Fi"] != nil {
		t.Fatalf("Wi-Fi = %v, want automatic", f.dns["Wi-Fi"])
	}
	if !slices.Equal(f.dns["USB 10/100/1000 LAN"], []string{"192.168.1.1", "8.8.8.8"}) {
		t.Fatalf("LAN = %v", f.dns["USB 10/100/1000 LAN"])
	}
}

func TestDarwinRestoreLeavesForeignChange(t *testing.T) {
	ctx := context.Background()
	f := newFakeMac()
	saved, _ := darwinSnapshot(ctx, f.run)
	darwinSet(ctx, f.run, TUNServers)
	f.dns["Wi-Fi"] = []string{"9.9.9.9"} // the user changed it meanwhile
	if err := darwinRestore(ctx, f.run, saved, TUNServers); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.dns["Wi-Fi"], []string{"9.9.9.9"}) {
		t.Fatalf("foreign change overwritten: %v", f.dns["Wi-Fi"])
	}
	if !slices.Equal(f.dns["USB 10/100/1000 LAN"], []string{"192.168.1.1", "8.8.8.8"}) {
		t.Fatalf("LAN not restored: %v", f.dns["USB 10/100/1000 LAN"])
	}
}

func TestDarwinRestoreCorrupt(t *testing.T) {
	if err := darwinRestore(context.Background(), newFakeMac().run, Saved("x"), TUNServers); err == nil {
		t.Fatal("corrupt snapshot must error")
	}
}
