package sysproxy

// macOS logic that does not need the OS itself (command planning and
// scutil output parsing) lives here without a build tag so it can be unit
// tested on any host. sysproxy_darwin.go wires it to the real executor.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// command is one networksetup invocation (arguments only; the binary is
// always networksetup).
type command []string

// parseNetworkServices extracts enabled service names from the output of
// `networksetup -listallnetworkservices`: the explanatory header is dropped
// and services prefixed with "*" (disabled) are skipped.
func parseNetworkServices(out string) []string {
	var services []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "*"):
		case strings.Contains(line, "asterisk") && strings.Contains(line, "denotes"):
		default:
			services = append(services, line)
		}
	}
	return services
}

// darwinBypass drops entries that only make sense on Windows ("<local>")
// and duplicates, preserving order.
func darwinBypass(bypass []string) []string {
	seen := make(map[string]struct{}, len(bypass))
	var out []string
	for _, b := range bypass {
		b = strings.TrimSpace(b)
		if b == "" || strings.EqualFold(b, "<local>") {
			continue
		}
		if _, dup := seen[b]; dup {
			continue
		}
		seen[b] = struct{}{}
		out = append(out, b)
	}
	return out
}

// setCommands returns the networksetup commands that point one service's
// HTTP, HTTPS and SOCKS proxies at host:port and install the bypass list.
func setCommands(service, host string, port int, bypass []string) []command {
	p := strconv.Itoa(port)
	cmds := []command{
		{"-setwebproxy", service, host, p},
		{"-setwebproxystate", service, "on"},
		{"-setsecurewebproxy", service, host, p},
		{"-setsecurewebproxystate", service, "on"},
		{"-setsocksfirewallproxy", service, host, p},
		{"-setsocksfirewallproxystate", service, "on"},
	}
	domains := darwinBypass(bypass)
	if len(domains) == 0 {
		// networksetup needs at least one argument; "Empty" clears the list.
		domains = []string{"Empty"}
	}
	cmds = append(cmds, append(command{"-setproxybypassdomains", service}, domains...))
	return cmds
}

// clearCommands returns the commands that turn the three proxies off for
// one service while leaving the stored host/port untouched.
func clearCommands(service string) []command {
	return []command{
		{"-setwebproxystate", service, "off"},
		{"-setsecurewebproxystate", service, "off"},
		{"-setsocksfirewallproxystate", service, "off"},
	}
}

// pacCommands returns the networksetup commands that point one service's
// automatic proxy configuration at url (which also turns it on), or turn
// it off when url is "".
func pacCommands(service, url string) []command {
	if url == "" {
		return []command{{"-setautoproxystate", service, "off"}}
	}
	return []command{
		{"-setautoproxyurl", service, url},
		{"-setautoproxystate", service, "on"},
	}
}

// darwinSetPAC implements SetPAC with the given executor.
func darwinSetPAC(ctx context.Context, run executor, url string) error {
	if url != "" && !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("PAC 地址无效：%s", url)
	}
	services, err := listServices(ctx, run)
	if err != nil {
		return err
	}
	what := "设置 PAC 代理"
	if url == "" {
		what = "关闭 PAC 代理"
	}
	return applyToServices(ctx, run, services, what, func(s string) []command { return pacCommands(s, url) })
}

// listServices runs networksetup -listallnetworkservices through run.
func listServices(ctx context.Context, run executor) ([]string, error) {
	out, err := run(ctx, "networksetup", "-listallnetworkservices")
	if err != nil {
		return nil, fmt.Errorf("无法列出网络服务：%w", err)
	}
	services := parseNetworkServices(out)
	if len(services) == 0 {
		return nil, errors.New("没有已启用的网络服务，无法设置系统代理。请在「系统设置 → 网络」中检查网络连接")
	}
	return services, nil
}

// applyToServices runs plan(service) for every service, collecting the
// first failure of each service into one Chinese error. Services after a
// failing one are still processed so a dead service (e.g. an unplugged
// adapter) does not block the others.
func applyToServices(ctx context.Context, run executor, services []string, what string, plan func(service string) []command) error {
	var failures []string
	for _, svc := range services {
		for _, c := range plan(svc) {
			if _, err := run(ctx, "networksetup", c...); err != nil {
				failures = append(failures, fmt.Sprintf("%s：%v", svc, err))
				break
			}
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("%s失败（%d/%d 个网络服务出错）：%s。请确认当前用户具有管理员权限后重试", what, len(failures), len(services), strings.Join(failures, "；"))
}

// darwinSet implements Set with the given executor.
func darwinSet(ctx context.Context, run executor, host string, port int, bypass []string) error {
	if host == "" || port <= 0 || port > 65535 {
		return fmt.Errorf("代理地址无效：%s", net.JoinHostPort(host, strconv.Itoa(port)))
	}
	services, err := listServices(ctx, run)
	if err != nil {
		return err
	}
	return applyToServices(ctx, run, services, "设置系统代理", func(s string) []command {
		return setCommands(s, host, port, bypass)
	})
}

// darwinClear implements Clear with the given executor.
func darwinClear(ctx context.Context, run executor) error {
	services, err := listServices(ctx, run)
	if err != nil {
		return err
	}
	return applyToServices(ctx, run, services, "关闭系统代理", clearCommands)
}

// darwinGet implements Get by parsing `scutil --proxy`.
func darwinGet(ctx context.Context, run executor) (State, error) {
	out, err := run(ctx, "scutil", "--proxy")
	if err != nil {
		return State{}, fmt.Errorf("无法读取系统代理设置：%w", err)
	}
	return parseScutilProxy(out), nil
}

// parseScutilProxy parses the dictionary printed by `scutil --proxy`.
// Enabled is true when any of HTTP/HTTPS/SOCKS is on; Host/Port come from
// the first enabled protocol in that order (falling back to any configured
// one), Bypass from ExceptionsList, PACURL from ProxyAutoConfigURLString
// when ProxyAutoConfigEnable is 1.
func parseScutilProxy(out string) State {
	values := map[string]string{}
	var exceptions []string
	inExceptions := false
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if inExceptions {
			if line == "}" {
				inExceptions = false
				continue
			}
			if _, v, ok := strings.Cut(line, " : "); ok {
				exceptions = append(exceptions, strings.TrimSpace(v))
			}
			continue
		}
		k, v, ok := strings.Cut(line, " : ")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if k == "ExceptionsList" {
			if strings.HasSuffix(v, "{") {
				inExceptions = true
			}
			continue
		}
		values[k] = v
	}

	st := State{Bypass: exceptions}
	if values["ProxyAutoConfigEnable"] == "1" {
		st.PACURL = values["ProxyAutoConfigURLString"]
	}
	type proto struct{ enable, host, port string }
	protos := []proto{
		{"HTTPEnable", "HTTPProxy", "HTTPPort"},
		{"HTTPSEnable", "HTTPSProxy", "HTTPSPort"},
		{"SOCKSEnable", "SOCKSProxy", "SOCKSPort"},
	}
	pick := func(p proto) {
		if st.Host != "" {
			return
		}
		if h := values[p.host]; h != "" {
			st.Host = h
			st.Port, _ = strconv.Atoi(values[p.port])
		}
	}
	for _, p := range protos {
		if values[p.enable] == "1" {
			st.Enabled = true
			pick(p)
		}
	}
	for _, p := range protos {
		pick(p)
	}
	return st
}
