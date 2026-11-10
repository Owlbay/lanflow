//go:build windows

package netinfo

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"lanflow/internal/model"
)

// prepareCommand hides the console window that netsh would otherwise flash.
func prepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

const ifTypePropVirtual = 53 // IF_TYPE_PROP_VIRTUAL (Wintun, Hyper-V and other virtual adapters)

// adapter is the subset of IP_ADAPTER_ADDRESSES we present.
type adapter struct {
	name    string
	ipv4    string
	ipv6    string
	gateway string
	dns     []string
	metric  uint32
	wifi    bool
	tunnel  bool
}

func get(parent context.Context) (model.NetInfo, error) {
	ctx, cancel := withBudget(parent)
	defer cancel()

	var info model.NetInfo
	a, err := primaryAdapter()
	if err != nil || a == nil {
		info.LANIP = lanAddress()
		if info.LANIP == "" {
			return info, errors.New("未检测到可用的网络连接")
		}
		return info, nil
	}
	info.Interface = a.name
	info.LANIP = a.ipv4
	info.IPv6 = a.ipv6
	info.Gateway = a.gateway
	info.DNS = a.dns
	if a.wifi {
		if out, err := runCommand(ctx, "netsh", "wlan", "show", "interfaces"); err == nil {
			info.SSID = parseNetshWlan(out, a.name)
		}
	}
	return info, nil
}

// primaryAdapter picks the up, non-loopback adapter that has an IPv4
// default gateway with the lowest IPv4 metric. Tunnel adapters (Mihomo's
// TUN uses 198.18.0.0/15) lose to physical ones so the LAN view stays
// meaningful while TUN mode is on.
func primaryAdapter() (*adapter, error) {
	adapters, err := listAdapters()
	if err != nil {
		return nil, err
	}
	var best *adapter
	for i := range adapters {
		a := &adapters[i]
		if a.gateway == "" {
			continue
		}
		if best == nil ||
			(best.tunnel && !a.tunnel) ||
			(best.tunnel == a.tunnel && a.metric < best.metric) {
			best = a
		}
	}
	return best, nil
}

func listAdapters() ([]adapter, error) {
	const flags = windows.GAA_FLAG_INCLUDE_GATEWAYS | windows.GAA_FLAG_SKIP_ANYCAST | windows.GAA_FLAG_SKIP_MULTICAST
	size := uint32(16 * 1024)
	var buf []byte
	for attempt := 0; attempt < 4; attempt++ {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, flags, 0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return nil, err
		}
		buf = nil
	}
	if buf == nil {
		return nil, windows.ERROR_BUFFER_OVERFLOW
	}
	if size == 0 {
		return nil, nil
	}

	var out []adapter
	for aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); aa != nil; aa = aa.Next {
		if aa.OperStatus != windows.IfOperStatusUp || aa.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK {
			continue
		}
		a := adapter{
			name:   windows.UTF16PtrToString(aa.FriendlyName),
			metric: aa.Ipv4Metric,
			wifi:   aa.IfType == windows.IF_TYPE_IEEE80211,
			tunnel: aa.IfType == windows.IF_TYPE_TUNNEL || aa.IfType == ifTypePropVirtual,
		}
		for u := aa.FirstUnicastAddress; u != nil; u = u.Next {
			ip := u.Address.IP()
			if v4 := ip.To4(); v4 != nil {
				if a.ipv4 == "" {
					a.ipv4 = v4.String()
				}
				if v4[0] == 198 && v4[1]&0xFE == 18 { // 198.18.0.0/15: Mihomo TUN
					a.tunnel = true
				}
			} else if a.ipv6 == "" && isPublicIPv6(ip) {
				a.ipv6 = ip.String()
			}
		}
		for g := aa.FirstGatewayAddress; g != nil; g = g.Next {
			ip := g.Address.IP()
			if v4 := ip.To4(); v4 != nil && !v4.IsUnspecified() {
				a.gateway = v4.String()
				break
			}
		}
		for d := aa.FirstDnsServerAddress; d != nil; d = d.Next {
			ip := d.Address.IP()
			if ip == nil || isWindowsPlaceholderDNS(ip) {
				continue
			}
			a.dns = append(a.dns, ip.String())
		}
		a.dns = dedup(a.dns)
		out = append(out, a)
	}
	return out, nil
}

// isWindowsPlaceholderDNS filters the fec0:0:0:ffff::1-3 site-local
// addresses Windows lists on adapters without configured DNS servers.
func isWindowsPlaceholderDNS(ip net.IP) bool {
	return ip.To4() == nil && len(ip) == net.IPv6len && ip[0] == 0xfe && ip[1] == 0xc0
}
