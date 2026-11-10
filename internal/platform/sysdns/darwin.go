package sysdns

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// executor runs a command and returns its combined output; tests replace it.
type executor func(ctx context.Context, name string, args ...string) (string, error)

const networksetup = "/usr/sbin/networksetup"

// darwinServices lists the enabled network services (disabled ones start
// with "*"; the first line is a header).
func darwinServices(ctx context.Context, run executor) ([]string, error) {
	out, err := run(ctx, networksetup, "-listallnetworkservices")
	if err != nil {
		return nil, fmt.Errorf("读取网络服务失败：%v", err)
	}
	var svcs []string
	for i, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if i == 0 && strings.Contains(line, "asterisk") || line == "" || strings.HasPrefix(line, "*") {
			continue
		}
		svcs = append(svcs, line)
	}
	return svcs, nil
}

// darwinGet returns a service's manual DNS servers (nil = automatic/DHCP).
func darwinGet(ctx context.Context, run executor, svc string) ([]string, error) {
	out, err := run(ctx, networksetup, "-getdnsservers", svc)
	if err != nil {
		return nil, err
	}
	var servers []string
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, " ") { // "There aren't any DNS Servers set on …"
			continue
		}
		servers = append(servers, line)
	}
	return servers, nil
}

func darwinSetOne(ctx context.Context, run executor, svc string, servers []string) error {
	args := []string{"-setdnsservers", svc}
	if len(servers) == 0 {
		args = append(args, "Empty")
	} else {
		args = append(args, servers...)
	}
	_, err := run(ctx, networksetup, args...)
	return err
}

func darwinSnapshot(ctx context.Context, run executor) (Saved, error) {
	svcs, err := darwinServices(ctx, run)
	if err != nil {
		return nil, err
	}
	m := map[string][]string{}
	for _, svc := range svcs {
		servers, err := darwinGet(ctx, run, svc)
		if err != nil {
			continue // a service we cannot read is also one we will not change
		}
		m[svc] = servers
	}
	return json.Marshal(m)
}

func darwinSet(ctx context.Context, run executor, servers []string) error {
	svcs, err := darwinServices(ctx, run)
	if err != nil {
		return err
	}
	var errs []error
	for _, svc := range svcs {
		if err := darwinSetOne(ctx, run, svc, servers); err != nil {
			errs = append(errs, fmt.Errorf("%s：%v", svc, err))
		}
	}
	if len(errs) > 0 && len(errs) == len(svcs) {
		return fmt.Errorf("设置系统 DNS 失败：%v", errors.Join(errs...))
	}
	flushCache(ctx, run)
	return nil
}

func darwinRestore(ctx context.Context, run executor, s Saved, ours []string) error {
	var m map[string][]string
	if err := json.Unmarshal(s, &m); err != nil {
		return fmt.Errorf("系统 DNS 快照损坏：%v", err)
	}
	var errs []error
	for svc, servers := range m {
		cur, err := darwinGet(ctx, run, svc)
		if err != nil || !slices.Equal(cur, ours) {
			continue // gone, or changed by someone else since
		}
		if err := darwinSetOne(ctx, run, svc, servers); err != nil {
			errs = append(errs, fmt.Errorf("%s：%v", svc, err))
		}
	}
	flushCache(ctx, run)
	if len(errs) > 0 {
		return fmt.Errorf("恢复系统 DNS 失败：%v", errors.Join(errs...))
	}
	return nil
}

func flushCache(ctx context.Context, run executor) {
	_, _ = run(ctx, "/usr/bin/dscacheutil", "-flushcache")
}
