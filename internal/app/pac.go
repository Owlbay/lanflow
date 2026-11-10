package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// PAC mode (设置 › 高级 › 系统代理): instead of a global HTTP/HTTPS/SOCKS
// proxy, the system is given the URL of a PAC file Lanflow serves on
// 127.0.0.1. The file sends the bypass list (and, when asked, mainland
// domains) DIRECT and everything else to the mixed port, so applications
// that honour PAC skip the proxy for those hosts themselves.

const pacPath = "/proxy.pac"

// pacServer is the tiny HTTP server behind the PAC URL. It serves the file
// built from the current settings on every request, so a changed bypass
// list or port needs no restart (the system re-fetches the PAC when the
// URL is set again).
type pacServer struct {
	mu   sync.Mutex
	ln   net.Listener
	srv  *http.Server
	url  string
	port int
}

// pacURL returns the URL the PAC server listens on, "" when it is stopped.
func (s *Service) pacURL() string {
	s.pac.mu.Lock()
	defer s.pac.mu.Unlock()
	return s.pac.url
}

// startPAC starts the PAC server on 127.0.0.1:port (model.PACDefaultPort
// when 0; any free port when that one is taken) and returns its URL. A
// running server on the same port is reused.
func (s *Service) startPAC(port int) (string, error) {
	if port <= 0 {
		port = model.PACDefaultPort
	}
	s.pac.mu.Lock()
	defer s.pac.mu.Unlock()
	if s.pac.ln != nil && s.pac.port == port {
		return s.pac.url, nil
	}
	s.stopPACLocked()
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		if ln, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
			return "", fmt.Errorf(i18n.T("无法启动 PAC 服务：%v"), err)
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc(pacPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(s.pacContent())
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	s.pac.ln, s.pac.srv, s.pac.port = ln, srv, port
	s.pac.url = "http://" + ln.Addr().String() + pacPath
	return s.pac.url, nil
}

// stopPAC shuts the PAC server down (no-op when stopped).
func (s *Service) stopPAC() {
	s.pac.mu.Lock()
	defer s.pac.mu.Unlock()
	s.stopPACLocked()
}

func (s *Service) stopPACLocked() {
	if s.pac.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = s.pac.srv.Shutdown(ctx)
		cancel()
	}
	s.pac.ln, s.pac.srv, s.pac.url, s.pac.port = nil, nil, "", 0
}

// pacContent builds the PAC file for the settings in force.
func (s *Service) pacContent() []byte {
	s.mu.Lock()
	port := s.state.settings.MixedPort
	directCN := s.state.settings.Advanced.PACDirectCN
	bypass := s.bypassLocked()
	s.mu.Unlock()
	return []byte(buildPAC(port, bypass, directCN))
}

// cnDirectSuffixes are the well-known mainland domains sent DIRECT by the
// 国内直连 option, besides every .cn name. A PAC file cannot consult
// GeoIP, so this is a short list of the sites nearly everyone uses rather
// than an attempt at completeness; the core's rules still decide for
// whatever reaches the proxy.
var cnDirectSuffixes = []string{
	"cn", "baidu.com", "bdstatic.com", "qq.com", "weixin.com", "wechat.com", "tencent.com", "gtimg.com",
	"taobao.com", "tmall.com", "alipay.com", "alicdn.com", "aliyun.com", "alibaba.com", "jd.com", "360buyimg.com",
	"163.com", "126.com", "netease.com", "sina.com.cn", "weibo.com", "sinaimg.cn", "sohu.com", "bilibili.com", "hdslb.com",
	"zhihu.com", "zhimg.com", "douyin.com", "bytedance.com", "toutiao.com", "feishu.cn", "larksuite.com", "dingtalk.com",
	"iqiyi.com", "youku.com", "mgtv.com", "meituan.com", "dianping.com", "xiaohongshu.com", "xhscdn.com", "pinduoduo.com",
	"douban.com", "csdn.net", "gitee.com", "huawei.com", "hicloud.com", "mi.com", "xiaomi.com", "oppo.com", "vivo.com",
	"ctrip.com", "12306.cn", "10086.cn", "189.cn", "10010.com", "cctv.com", "ifeng.com", "360.com", "qihoo.com", "sogou.com",
}

// buildPAC renders the PAC file: DIRECT for the bypass list (plain host
// names for "<local>", wildcards, exact hosts and their subdomains, IP
// literals and ranges), DIRECT for mainland domains when directCN, and the
// mixed port for everything else with DIRECT as the last resort.
func buildPAC(port int, bypass []string, directCN bool) string {
	var b strings.Builder
	b.WriteString("// Lanflow PAC: generated from the system proxy bypass list.\n")
	b.WriteString("function FindProxyForURL(url, host) {\n")
	b.WriteString("  host = host.toLowerCase();\n")
	var hostRules, ipRules []string
	for _, raw := range bypass {
		e := strings.ToLower(strings.TrimSpace(raw))
		switch {
		case e == "":
		case e == "<local>":
			hostRules = append(hostRules, "isPlainHostName(host)")
		case strings.Contains(e, "/"):
			if p, err := netip.ParsePrefix(e); err == nil && p.Addr().Is4() {
				mask := net.CIDRMask(p.Bits(), 32)
				ipRules = append(ipRules, fmt.Sprintf("isInNet(host, %s, %s)", jsStr(p.Masked().Addr().String()), jsStr(net.IP(mask).String())))
			}
		case strings.ContainsAny(e, "*?"):
			hostRules = append(hostRules, "shExpMatch(host, "+jsStr(e)+")")
		default:
			if a, err := netip.ParseAddr(strings.Trim(e, "[]")); err == nil {
				ipRules = append(ipRules, "host == "+jsStr(a.String()))
				continue
			}
			d := strings.TrimPrefix(e, ".")
			hostRules = append(hostRules, "host == "+jsStr(d)+" || dnsDomainIs(host, "+jsStr("."+d)+")")
		}
	}
	if directCN {
		for _, d := range cnDirectSuffixes {
			hostRules = append(hostRules, "host == "+jsStr(d)+" || dnsDomainIs(host, "+jsStr("."+d)+")")
		}
	}
	if len(hostRules) > 0 {
		b.WriteString("  if (" + strings.Join(hostRules, " ||\n      ") + ") return \"DIRECT\";\n")
	}
	if len(ipRules) > 0 {
		b.WriteString("  if (/^[\\d.]+$|:/.test(host)) {\n")
		b.WriteString("    if (" + strings.Join(ipRules, " ||\n        ") + ") return \"DIRECT\";\n")
		b.WriteString("  }\n")
	}
	hp := "127.0.0.1:" + strconv.Itoa(port)
	fmt.Fprintf(&b, "  return \"PROXY %s; SOCKS5 %s; DIRECT\";\n}\n", hp, hp)
	return b.String()
}

// jsStr quotes s as a JavaScript string literal.
func jsStr(s string) string { return strconv.Quote(s) }
