package ctl

import (
	"fmt"
	"net"
	"strings"

	"lanflow/internal/app/remote"
	"lanflow/internal/applog"
	"lanflow/internal/model"
)

// Diagnosis (`lanflow ctl diag`, and the MCP tools built on it): a
// snapshot of the state that explains most "it does not work" reports,
// with findings in plain words. Only reads, unless run asks for the site
// and leak tests first.

// DiagReport is the `diag` output.
type DiagReport struct {
	Status    StatusView        `json:"status"`
	Network   model.NetInfo     `json:"network"`
	DNS       DiagDNS           `json:"dns"`
	Sites     []model.SiteCheck `json:"sites"`
	Leak      model.LeakResult  `json:"leak"`
	RecentLog []model.LogEntry  `json:"recentLog,omitempty"` // warnings and errors of Lanflow's own log
	Findings  []string          `json:"findings"`
}

// DiagDNS summarizes the DNS in force.
type DiagDNS struct {
	Source       string   `json:"source"`
	Enabled      bool     `json:"enabled"`
	EnhancedMode string   `json:"enhancedMode,omitempty"`
	Nameserver   []string `json:"nameserver,omitempty"`
}

// Diagnose collects the report; run tests the sites and leaks first.
func Diagnose(c *remote.Client, run bool) (DiagReport, error) {
	var rep DiagReport
	if run {
		if err := c.TestSites(); err != nil {
			rep.Findings = append(rep.Findings, "网站连通性测试失败: "+err.Error())
		}
		if err := c.RunLeakCheck(); err != nil {
			rep.Findings = append(rep.Findings, "泄漏检查无法完成: "+err.Error())
		}
	}
	st := c.Status()
	if err := c.LastError(); err != nil {
		return rep, err
	}
	rep.Status = statusView(st, c.Subscriptions(), c.Proxies())
	rep.Network = c.NetInfo()
	d := c.EffectiveDNS()
	rep.DNS = DiagDNS{Source: d.Source, Enabled: d.Enabled, EnhancedMode: d.Settings.EnhancedMode, Nameserver: d.Settings.Nameserver}
	for i, ns := range rep.DNS.Nameserver {
		rep.DNS.Nameserver[i] = applog.Redact(ns)
	}
	rep.Sites = c.Sites()
	rep.Leak = c.LeakCheck()
	logs := c.AppLogs()
	for i := len(logs) - 1; i >= 0 && len(rep.RecentLog) < 10; i-- {
		if logs[i].Level == applog.Warn || logs[i].Level == applog.Error {
			rep.RecentLog = append([]model.LogEntry{logs[i]}, rep.RecentLog...)
		}
	}
	rep.Findings = append(rep.Findings, findings(st, rep)...)
	if len(rep.Findings) == 0 {
		rep.Findings = []string{"没有发现明显问题"}
	}
	return rep, c.LastError()
}

func findings(st model.Status, rep DiagReport) []string {
	var out []string
	switch st.Core {
	case model.CoreRunning:
	case model.CoreStopped:
		out = append(out, "内核没有运行：所有代理都不可用（lanflow ctl call StartCore 启动）")
	default:
		out = append(out, fmt.Sprintf("内核状态为 %s：%s", st.Core, st.CoreError))
	}
	if st.Core == model.CoreRunning && !st.SystemProxy && !st.TUN {
		out = append(out, "系统代理和 TUN 都没有开启：只有手动设置了代理的应用会经过澜流")
	}
	if !st.HasSubscriptions {
		out = append(out, "还没有任何订阅")
	}
	if st.Mode == model.ModeDirect {
		out = append(out, "出站模式是直连：所有流量都不经过代理")
	}
	failProxy, failDirect := 0, 0
	for _, s := range rep.Sites {
		if s.ViaProxy == -1 {
			failProxy++
		}
		if s.Direct == -1 {
			failDirect++
		}
	}
	if n := len(rep.Sites); n > 0 && failProxy == n {
		out = append(out, "所有测试网站经代理都无法访问：当前节点可能不可用，试试换节点或更新订阅")
	} else if failProxy > 0 {
		out = append(out, fmt.Sprintf("%d 个测试网站经代理无法访问", failProxy))
	}
	if n := len(rep.Sites); n > 0 && failDirect == n {
		out = append(out, "所有测试网站直连都无法访问：本机网络可能断开")
	}
	for _, it := range rep.Leak.Items {
		if it.Status == model.LeakFail || it.Status == model.LeakWarn {
			out = append(out, fmt.Sprintf("泄漏检查 %s：%s", it.Name, it.Summary))
		}
	}
	if rep.Network.PublicIPStatus == "failed" {
		out = append(out, "无法获取直连公网 IP："+rep.Network.PublicIPError)
	}
	return out
}

// SiteReport is the diagnosis of one host.
type SiteReport struct {
	Host     string           `json:"host"`
	Rule     model.RuleMatch  `json:"rule"`
	DNS      *model.DNSResult `json:"dns,omitempty"`
	Exit     string           `json:"exit,omitempty"`  // the node the chain ends at
	Delay    int              `json:"delay,omitempty"` // that node's delay after a test, ms; -1 failed
	Status   StatusView       `json:"status"`
	Findings []string         `json:"findings"`
}

// DiagnoseSite explains why host may not load: which rule and node it
// takes, whether DNS answers, whether that node works.
func DiagnoseSite(c *remote.Client, host string) (SiteReport, error) {
	host = CleanHost(host)
	rep := SiteReport{Host: host}
	st := c.Status()
	if err := c.LastError(); err != nil {
		return rep, err
	}
	px := c.Proxies()
	rep.Status = statusView(st, c.Subscriptions(), px)
	rep.Findings = findings(st, DiagReport{})
	m, err := c.TestRule(host)
	if err != nil {
		rep.Findings = append(rep.Findings, "无法测试规则: "+err.Error())
	} else {
		rep.Rule = m
		if strings.EqualFold(m.Policy, "REJECT") || strings.HasPrefix(strings.ToUpper(m.Policy), "REJECT") {
			rep.Findings = append(rep.Findings, fmt.Sprintf("%s 命中规则 %s，被拒绝（REJECT）：这是规则有意拦截的", host, m.Rule))
		}
	}
	if net.ParseIP(host) == nil {
		if r, err := c.DNSQuery(host, "A"); err != nil {
			rep.Findings = append(rep.Findings, "DNS 查询失败: "+err.Error())
		} else {
			rep.DNS = &r
			if len(r.Answers) == 0 && len(r.AppAnswers) == 0 {
				rep.Findings = append(rep.Findings, fmt.Sprintf("DNS 没有返回地址（%s）：域名可能拼错或被污染", r.Status))
			}
		}
	}
	if n := len(m.Chain); n > 0 {
		rep.Exit = m.Chain[n-1]
	}
	if rep.Exit != "" && !strings.EqualFold(rep.Exit, "DIRECT") && !strings.HasPrefix(strings.ToUpper(rep.Exit), "REJECT") {
		if err := c.TestProxyDelay(rep.Exit); err == nil {
			rep.Delay = c.Proxies().Proxies[rep.Exit].Delay
			if rep.Delay < 0 {
				rep.Findings = append(rep.Findings, fmt.Sprintf("出口节点「%s」测速失败：换一个节点（lanflow ctl proxies select）或更新订阅", rep.Exit))
			}
		}
	}
	if strings.EqualFold(rep.Exit, "DIRECT") && st.Mode != model.ModeDirect {
		rep.Findings = append(rep.Findings, fmt.Sprintf("%s 按规则直连：如果它需要代理，可以添加规则让它走代理组", host))
	}
	if len(rep.Findings) == 0 {
		rep.Findings = []string{"规则、DNS 和出口节点都正常；问题可能在网站本身或应用没有经过澜流（检查系统代理或 TUN）"}
	}
	return rep, nil
}

// CleanHost reduces a URL or host:port to the host.
func CleanHost(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	return strings.ToLower(strings.TrimSuffix(s, "."))
}
