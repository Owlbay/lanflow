package sysproxy

// Platform-independent snapshot logic (parsing and command planning) so it
// can be unit tested on any host. snapshot_<os>.go wires it to the system.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// darwinProxy is one of a service's three proxies as networksetup reports it.
type darwinProxy struct {
	On     bool   `json:"on"`
	Server string `json:"server,omitempty"`
	Port   int    `json:"port,omitempty"`
}

// darwinAuto is a service's automatic proxy configuration (PAC).
type darwinAuto struct {
	On  bool   `json:"on"`
	URL string `json:"url,omitempty"`
}

// darwinService is the saved proxy configuration of one network service.
// Auto is nil in snapshots written before PAC support; restore then
// leaves the PAC setting alone.
type darwinService struct {
	Name   string      `json:"name"`
	Web    darwinProxy `json:"web"`
	Secure darwinProxy `json:"secure"`
	Socks  darwinProxy `json:"socks"`
	Bypass []string    `json:"bypass,omitempty"`
	Auto   *darwinAuto `json:"auto,omitempty"`
}

type darwinSaved struct {
	OS       string          `json:"os"`
	Services []darwinService `json:"services"`
}

// parseGetProxy parses `networksetup -getwebproxy <service>` (and the
// secure/socks variants):
//
//	Enabled: Yes
//	Server: 127.0.0.1
//	Port: 7890
//	Authenticated Proxy Enabled: 0
func parseGetProxy(out string) darwinProxy {
	var p darwinProxy
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "Enabled":
			p.On = strings.EqualFold(v, "Yes")
		case "Server":
			p.Server = v
		case "Port":
			p.Port, _ = strconv.Atoi(v)
		}
	}
	return p
}

// parseGetAutoProxy parses `networksetup -getautoproxyurl <service>`:
//
//	URL: http://127.0.0.1:11233/proxy.pac
//	Enabled: Yes
func parseGetAutoProxy(out string) darwinAuto {
	var a darwinAuto
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "Enabled":
			a.On = strings.EqualFold(v, "Yes")
		case "URL":
			a.URL = v
		}
	}
	if a.URL == "(null)" {
		a.URL = ""
	}
	return a
}

// parseBypassDomains parses `networksetup -getproxybypassdomains <service>`:
// one domain per line, or a sentence when the list is empty.
func parseBypassDomains(out string) []string {
	var ds []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, " ") {
			continue // "There aren't any bypass domains set on Wi-Fi."
		}
		ds = append(ds, line)
	}
	return ds
}

// darwinSnapshot reads the three proxies and bypass list of every enabled service.
func darwinSnapshot(ctx context.Context, run executor) (Saved, error) {
	services, err := listServices(ctx, run)
	if err != nil {
		return nil, err
	}
	saved := darwinSaved{OS: "darwin"}
	for _, svc := range services {
		ds := darwinService{Name: svc}
		for _, it := range []struct {
			flag string
			dst  *darwinProxy
		}{{"-getwebproxy", &ds.Web}, {"-getsecurewebproxy", &ds.Secure}, {"-getsocksfirewallproxy", &ds.Socks}} {
			out, err := run(ctx, "networksetup", it.flag, svc)
			if err != nil {
				return nil, fmt.Errorf("无法读取「%s」的代理设置：%w", svc, err)
			}
			*it.dst = parseGetProxy(out)
		}
		if out, err := run(ctx, "networksetup", "-getproxybypassdomains", svc); err == nil {
			ds.Bypass = parseBypassDomains(out)
		}
		if out, err := run(ctx, "networksetup", "-getautoproxyurl", svc); err == nil {
			a := parseGetAutoProxy(out)
			ds.Auto = &a
		}
		saved.Services = append(saved.Services, ds)
	}
	return json.Marshal(saved)
}

// restoreCommands plans the networksetup calls that put one saved service back.
func restoreCommands(ds darwinService) []command {
	var cmds []command
	for _, it := range []struct {
		set, state string
		p          darwinProxy
	}{{"-setwebproxy", "-setwebproxystate", ds.Web}, {"-setsecurewebproxy", "-setsecurewebproxystate", ds.Secure}, {"-setsocksfirewallproxy", "-setsocksfirewallproxystate", ds.Socks}} {
		if it.p.Server != "" && it.p.Port > 0 {
			// Setting the server also turns the proxy on; fix the state after.
			cmds = append(cmds, command{it.set, ds.Name, it.p.Server, strconv.Itoa(it.p.Port)})
		}
		state := "off"
		if it.p.On && it.p.Server != "" {
			state = "on"
		}
		cmds = append(cmds, command{it.state, ds.Name, state})
	}
	domains := ds.Bypass
	if len(domains) == 0 {
		domains = []string{"Empty"}
	}
	cmds = append(cmds, append(command{"-setproxybypassdomains", ds.Name}, domains...))
	if ds.Auto != nil {
		if ds.Auto.URL != "" {
			cmds = append(cmds, command{"-setautoproxyurl", ds.Name, ds.Auto.URL})
		}
		state := "off"
		if ds.Auto.On && ds.Auto.URL != "" {
			state = "on"
		}
		cmds = append(cmds, command{"-setautoproxystate", ds.Name, state})
	}
	return cmds
}

// darwinRestore puts back a darwinSaved; services not in it are cleared.
func darwinRestore(ctx context.Context, run executor, s Saved) error {
	var saved darwinSaved
	if err := json.Unmarshal(s, &saved); err != nil || saved.OS != "darwin" {
		return errors.New("保存的系统代理设置无法识别")
	}
	services, err := listServices(ctx, run)
	if err != nil {
		return err
	}
	byName := make(map[string]darwinService, len(saved.Services))
	for _, ds := range saved.Services {
		byName[ds.Name] = ds
	}
	return applyToServices(ctx, run, services, "恢复系统代理", func(svc string) []command {
		if ds, ok := byName[svc]; ok {
			return restoreCommands(ds)
		}
		return clearCommands(svc)
	})
}

// windowsSaved holds the per-user WinINet values (nil = value absent).
// HasAutoConfig marks snapshots that recorded AutoConfigURL (the PAC
// address); older ones leave it alone on restore.
type windowsSaved struct {
	OS            string  `json:"os"`
	Enable        *uint64 `json:"enable,omitempty"`
	Server        *string `json:"server,omitempty"`
	Override      *string `json:"override,omitempty"`
	AutoConfig    *string `json:"autoConfig,omitempty"`
	HasAutoConfig bool    `json:"hasAutoConfig,omitempty"`
}
