// Package netinfo reads local network information.
//
// Exported signatures are a stable contract. Implementations: darwin (route get default, scutil --dns, networksetup
// -getairportnetwork / ipconfig getsummary for SSID, net.Interfaces) and
// windows (GetAdaptersAddresses via x/sys/windows, netsh wlan show interfaces).
package netinfo

import (
	"context"

	"lanflow/internal/model"
)

// Get returns the primary interface, LAN IP, gateway, DNS servers and
// SSID. Fields it cannot determine are left empty. Must finish in < 3 s.
func Get(ctx context.Context) (model.NetInfo, error) { return get(ctx) }

// OtherTUN reports a TUN interface that currently captures the default
// route (split routes such as 1/8…128.0/1 or 0/1, as Mihomo and
// most VPN-style clients install). Call it while Lanflow's own TUN is off:
// a hit means another proxy client's TUN is active, and two TUNs fighting
// over routes and DNS break the network. name is the interface ("utun1500")
// or its address on Windows.
func OtherTUN(ctx context.Context) (name string, ok bool) { return otherTUN(ctx) }

// LANAddress returns the primary IPv4 LAN address, "" if none.
func LANAddress() string { return lanAddress() }
