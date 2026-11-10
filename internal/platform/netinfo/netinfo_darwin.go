//go:build darwin

package netinfo

import (
	"context"
	"errors"
	"os/exec"
	"sync"

	"lanflow/internal/model"
)

func prepareCommand(*exec.Cmd) {}

// get gathers the macOS network picture: default route first (it decides
// the interface), then hardware port names, DNS and SSID concurrently so
// the whole call stays well inside the 3 s budget.
func get(parent context.Context) (model.NetInfo, error) {
	ctx, cancel := withBudget(parent)
	defer cancel()

	var info model.NetInfo
	route := primaryRoute(ctx)
	info.Gateway = route.Gateway
	info.Interface = route.Interface
	info.LANIP, info.IPv6 = interfaceAddrs(route.Interface)
	if info.LANIP == "" {
		info.LANIP = lanAddress()
	}

	var (
		wg       sync.WaitGroup
		portName string
		dns      []string
		summary  ipconfigSummary
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		if out, err := runCommand(ctx, "networksetup", "-listallhardwareports"); err == nil {
			portName = parseHardwarePorts(out)[route.Interface]
		}
	}()
	go func() {
		defer wg.Done()
		if out, err := runCommand(ctx, "scutil", "--dns"); err == nil {
			dns = parseScutilDNS(out)
		}
	}()
	if route.Interface != "" && !isTunnelName(route.Interface) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if out, err := runCommand(ctx, "ipconfig", "getsummary", route.Interface); err == nil {
				summary = parseIPConfigSummary(out)
			}
		}()
	}
	wg.Wait()

	info.DNS = dns
	if portName != "" {
		info.Interface = portName + " (" + route.Interface + ")"
	}
	info.SSID = summary.SSID
	if info.SSID == "" && (summary.WiFi || isWiFiPort(portName)) && ctx.Err() == nil {
		// ipconfig redacts the SSID without Location Services permission;
		// networksetup may still report it (or also redact it).
		if out, err := runCommand(ctx, "networksetup", "-getairportnetwork", route.Interface); err == nil {
			info.SSID = parseAirportNetwork(out)
		}
	}

	if info.Interface == "" && info.LANIP == "" {
		if err := ctx.Err(); err != nil {
			return info, errors.New("读取本机网络信息超时")
		}
		return info, errors.New("未检测到可用的网络连接")
	}
	return info, nil
}

// primaryRoute returns the default route; when it goes through a tunnel
// (TUN mode active) the underlying physical default route from netstat is
// preferred so the UI shows the real LAN gateway.
func primaryRoute(ctx context.Context) defaultRoute {
	var route defaultRoute
	if out, err := runCommand(ctx, "route", "-n", "get", "default"); err == nil {
		route = parseRouteDefault(out)
	}
	if route.Interface == "" || isTunnelName(route.Interface) {
		if out, err := runCommand(ctx, "netstat", "-rn", "-f", "inet"); err == nil {
			for _, r := range parseNetstatDefaults(out) {
				if r.Interface != "" && !isTunnelName(r.Interface) {
					return r
				}
			}
		}
	}
	return route
}
