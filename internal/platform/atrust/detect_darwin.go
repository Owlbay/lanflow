//go:build darwin

package atrust

import (
	"context"
	"net/netip"
	"os/exec"
	"strings"
	"time"
)

func run(ctx context.Context, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, name, args...).Output()
	return string(out)
}

func detect(ctx context.Context, own func(string) bool) Info {
	procs := matchProcesses(withoutSelf(strings.Split(run(ctx, "/bin/ps", "-axo", "pid=,comm="), "\n")))
	if len(procs) == 0 {
		return Info{}
	}
	table := run(ctx, "/usr/sbin/netstat", "-rn", "-f", "inet")
	full := fullTunnels(table)
	skip := func(i string) bool { return own(i) || full[i] }
	info := build(procs, parseRoutes(table, own), parseResolvers(run(ctx, "/usr/sbin/scutil", "--dns")), skip)
	var ranges []netip.Prefix
	for _, c := range info.CIDRs {
		if p, err := netip.ParsePrefix(c); err == nil {
			ranges = append(ranges, p)
		}
	}
	socks := run(ctx, "/usr/sbin/netstat", "-anv", "-p", "tcp") + run(ctx, "/usr/sbin/netstat", "-anv", "-p", "udp")
	info.Gateways = parseGateways(socks, ranges)
	return info
}
