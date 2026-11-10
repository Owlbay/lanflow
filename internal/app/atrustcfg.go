package app

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// The user's aTrust 兼容 lists (model.ATrustConfig): a commented reference
// template, import from Lanflow's own YAML/JSON or from a JavaScript
// override script (such as atrust-coexist.js), and export. Nothing
// here carries any company's addresses: the template uses documentation
// values and the import only reads what the file holds.

// atrustMaxFile bounds the file read by ImportATrustConfig.
const atrustMaxFile = 1 << 20

// atrustReference is the reference template; every line of it is
// translated as one text, so the comments read as a whole.
const atrustReference = `# 澜流 · aTrust 兼容配置（参考模板）
# 用法：在「设置 › 代理 › aTrust 兼容 › 配置…」中按行填写；或把本文件改好后用「导入…」载入，确认后保存。
# 下面的值全是示例（corp.example.com、10.x、203.0.113.x），请换成你公司的实际地址。
# 澜流不内置任何公司的地址：留空的项不生效，自动检测照常进行。
#
# 自动检测与本配置叠加：aTrust 运行时，澜流从它的路由表和解析器读到的网段、域名、DNS 和网关会自动加入；
# 这里填的是检测不到的部分，例如 aTrust 未运行时也要走公司 DNS 的域名。

# 未运行 aTrust 时也应用这些列表。在公司办公网不开 aTrust 时，内网域名仍需要公司 DNS 才能解析。
always: false

# 内网域名后缀：直接连接、不分配 fake-ip、不嗅探，由 corp-dns 解析。
# 只放真正的内网域名；公网 DNS 也能解析出同样结果的域名请放到 direct-domains。
corp-domains:
  - corp.example.com
  - intranet.example.net

# 需要直连、但绝不能走公司 DNS 的公网域名（钉钉、企业微信之类的 SaaS）。
# 不在办公网时公司 DNS 只能经 aTrust 到达，走它会超时，这些域名就全打不开；它们由公网 DNS（纯 UDP）解析。
direct-domains:
  - chat.example.org
  - docs.example.org

# 公司 DNS 服务器（IP）。留空时使用检测到的 aTrust 解析器地址。
corp-dns:
  - 10.1.0.53
  - 10.1.0.54

# 不进入 TUN 的网段：写入 tun.route-exclude-address，交给系统路由（aTrust）处理，并直接连接。
# 适合放：aTrust 的映射地址段（常见 198.18.0.0/16，只排 /16，不要写成 /15）、aTrust 网关的 /32、
# 只能经 aTrust 到达的内网段。
# 切勿放入包含任何 DNS 服务器的网段：例如公司 DNS 是 10.1.0.53 时不要排除 10.0.0.0/8。
# 排除后发往它的 DNS 查询绕开了内核，DNS 劫持失效，被墙网站会全部打不开。这类网段请放到 direct-cidrs。
# aTrust 运行时它的网关会被自动检测并排除，不必手填；检测到的网段若含 DNS 服务器也会自动改为仅直连。
exclude-cidrs:
  - 198.18.0.0/16
  - 172.16.0.0/12
  - 203.0.113.10/32

# 仅直连的网段：直接连接，但仍经过 TUN，发往其中 DNS 服务器的查询照常被内核接管。
direct-cidrs:
  - 10.0.0.0/8
`

// ATrustReferenceYAML is the reference template in the current language.
func ATrustReferenceYAML() string { return i18n.T(atrustReference) }

// ATrustReference implements API.
func (s *Service) ATrustReference() string { return ATrustReferenceYAML() }

// ImportATrustConfig implements API.
func (s *Service) ImportATrustConfig(path string) (model.ATrustConfig, string, error) {
	cfg, summary, err := ReadATrustConfig(path)
	if err != nil {
		s.alog.Errorf("import aTrust config: %v", err)
		return cfg, "", err
	}
	s.alog.Infof("aTrust config read from %s: %s", filepath.Base(path), summary)
	return cfg, summary, nil
}

// ExportATrustConfig implements API.
func (s *Service) ExportATrustConfig(path string) error {
	if err := WriteATrustConfig(path, s.Settings().ATrust); err != nil {
		s.alog.Errorf("export aTrust config: %v", err)
		return err
	}
	s.alog.Infof("aTrust config exported to %s", filepath.Base(path))
	return nil
}

// atrustFile is Lanflow's YAML form of model.ATrustConfig.
type atrustFile struct {
	Always        bool     `yaml:"always"`
	CorpDomains   []string `yaml:"corp-domains,omitempty"`
	DirectDomains []string `yaml:"direct-domains,omitempty"`
	CorpDNS       []string `yaml:"corp-dns,omitempty"`
	ExcludeCIDRs  []string `yaml:"exclude-cidrs,omitempty"`
	DirectCIDRs   []string `yaml:"direct-cidrs,omitempty"`
}

// WriteATrustConfig writes cfg to path in Lanflow's YAML format.
func WriteATrustConfig(path string, cfg model.ATrustConfig) error {
	f := atrustFile{Always: cfg.Always, CorpDomains: cfg.CorpDomains, DirectDomains: cfg.DirectDomains,
		CorpDNS: cfg.CorpDNS, ExcludeCIDRs: cfg.ExcludeCIDRs, DirectCIDRs: cfg.DirectCIDRs}
	body, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	head := "# " + i18n.T("澜流 aTrust 兼容配置。在「设置 › 代理 › aTrust 兼容 › 配置… › 导入…」中载入；各项含义见「复制参考配置」。") + "\n"
	if err := os.WriteFile(path, []byte(head+string(body)), 0o600); err != nil {
		return fmt.Errorf(i18n.T("写入 %s 失败：%v"), filepath.Base(path), err)
	}
	return nil
}

// ReadATrustConfig reads a configuration file: Lanflow's YAML/JSON, or a
// JavaScript override script such as atrust-coexist.js.
// Entries are validated and deduplicated; the summary says what was found.
func ReadATrustConfig(path string) (model.ATrustConfig, string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return model.ATrustConfig{}, "", fmt.Errorf(i18n.T("读取 %s 失败：%v"), filepath.Base(path), err)
	}
	if fi.Size() > atrustMaxFile {
		return model.ATrustConfig{}, "", fmt.Errorf(i18n.T("%s 过大（%d 字节），不像是 aTrust 配置文件"), filepath.Base(path), fi.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return model.ATrustConfig{}, "", fmt.Errorf(i18n.T("读取 %s 失败：%v"), filepath.Base(path), err)
	}
	var raw rawATrust
	if isJSOverride(path, data) {
		raw = parseATrustJS(string(data))
	} else if raw, err = parseATrustYAML(data); err != nil {
		return model.ATrustConfig{}, "", fmt.Errorf(i18n.T("%s 不是有效的 YAML/JSON：%v"), filepath.Base(path), err)
	}
	cfg, skipped := raw.validate()
	if cfg.Empty() {
		return model.ATrustConfig{}, "", errors.New(i18n.T("文件里没有可识别的 aTrust 配置。支持澜流的 YAML/JSON 格式，以及 JavaScript 覆写脚本（如 atrust-coexist.js）。"))
	}
	summary := i18n.Tf("已从 %s 读取：内网域名 %d 个、直连域名 %d 个、公司 DNS %d 个、不进入 TUN 的网段 %d 个、仅直连的网段 %d 个。",
		filepath.Base(path), len(cfg.CorpDomains), len(cfg.DirectDomains), len(cfg.CorpDNS), len(cfg.ExcludeCIDRs), len(cfg.DirectCIDRs))
	if skipped > 0 {
		summary += i18n.Tf("忽略了 %d 项无法识别的条目。", skipped)
	}
	return cfg, summary, nil
}

// isJSOverride tells a JavaScript override script from a YAML/JSON file.
func isJSOverride(path string, data []byte) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js", ".mjs", ".cjs":
		return true
	case ".yaml", ".yml", ".json":
		return false
	}
	return strings.Contains(string(data), "function main(") || strings.Contains(string(data), "const ")
}

// rawATrust holds the unvalidated entries read from a file.
type rawATrust struct {
	always                              bool
	corpDomains, directDomains, corpDNS []string
	excludeCIDRs, directCIDRs           []string
	ruleCIDRs                           []string // IP-CIDR rule payloads (JS): direct unless excluded
}

// parseATrustYAML reads Lanflow's YAML form, also accepting the JSON of
// model.ATrustConfig (keys compared without case, "-" and "_").
func parseATrustYAML(data []byte) (rawATrust, error) {
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return rawATrust{}, err
	}
	var raw rawATrust
	for k, v := range m {
		switch normKey(k) {
		case "always":
			b, _ := v.(bool)
			raw.always = b
		case "corpdomains":
			raw.corpDomains = anyStrings(v)
		case "directdomains":
			raw.directDomains = anyStrings(v)
		case "corpdns":
			raw.corpDNS = anyStrings(v)
		case "excludecidrs":
			raw.excludeCIDRs = anyStrings(v)
		case "directcidrs":
			raw.directCIDRs = anyStrings(v)
		}
	}
	return raw, nil
}

func normKey(k string) string {
	return strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(k))
}

func anyStrings(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
	case string:
		for _, l := range strings.Split(x, "\n") {
			out = append(out, l)
		}
	case nil:
	default:
		out = append(out, fmt.Sprint(x))
	}
	return out
}

var (
	jsArrayRe  = regexp.MustCompile(`(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*\[([^\]]*)\]`)
	jsStringRe = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)'`)
	ipRuleRe   = regexp.MustCompile(`(?i)^IP-CIDR6?,([^,]+),DIRECT`)
)

// parseATrustJS reads the arrays of a JavaScript override script:
// atrustDns → CorpDNS, corpDomains → CorpDomains, publicSaasDomains →
// DirectDomains, atrustCIDRs → ExcludeCIDRs, and every IP-CIDR DIRECT rule
// string → DirectCIDRs (unless already excluded). Names are matched
// without case; a few synonyms are accepted.
func parseATrustJS(src string) rawATrust {
	src = stripJSComments(src)
	var raw rawATrust
	for _, m := range jsArrayRe.FindAllStringSubmatch(src, -1) {
		items := jsStrings(m[2])
		switch strings.ToLower(m[1]) {
		case "atrustdns", "corpdns", "companydns", "intranetdns":
			raw.corpDNS = append(raw.corpDNS, items...)
		case "corpdomains", "intranetdomains", "atrustdomains":
			raw.corpDomains = append(raw.corpDomains, items...)
		case "publicsaasdomains", "saasdomains", "directdomains", "publicdomains":
			raw.directDomains = append(raw.directDomains, items...)
		case "atrustcidrs", "excludecidrs", "routeexclude", "excluderoutes":
			raw.excludeCIDRs = append(raw.excludeCIDRs, items...)
		case "directcidrs":
			raw.directCIDRs = append(raw.directCIDRs, items...)
		}
	}
	for _, s := range jsStrings(src) {
		if m := ipRuleRe.FindStringSubmatch(s); m != nil {
			raw.ruleCIDRs = append(raw.ruleCIDRs, strings.TrimSpace(m[1]))
		}
	}
	return raw
}

// jsStrings returns the contents of the string literals in s.
func jsStrings(s string) []string {
	var out []string
	for _, m := range jsStringRe.FindAllStringSubmatch(s, -1) {
		v := m[1]
		if m[1] == "" {
			v = m[2]
		}
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// stripJSComments removes // and /* */ comments, leaving string and
// template literals alone (a URL holds "//").
func stripJSComments(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '"' || c == '\'' || c == '`':
			j := i + 1
			for j < len(src) && src[j] != c {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			if j < len(src) {
				j++
			}
			b.WriteString(src[i:j])
			i = j
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			i += end + 4
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// validate normalises and checks every entry, dropping invalid ones and
// duplicates; it returns the configuration and the number dropped.
func (r rawATrust) validate() (model.ATrustConfig, int) {
	cfg := model.ATrustConfig{Always: r.always}
	skipped := 0
	fill := func(dst *[]string, in []string, norm func(string) (string, bool)) {
		for _, s := range in {
			v, ok := norm(s)
			if !ok {
				if strings.TrimSpace(s) != "" {
					skipped++
				}
				continue
			}
			if !containsStr(*dst, v) {
				*dst = append(*dst, v)
			}
		}
	}
	fill(&cfg.CorpDomains, r.corpDomains, NormDomain)
	fill(&cfg.DirectDomains, r.directDomains, NormDomain)
	fill(&cfg.CorpDNS, r.corpDNS, NormIP)
	fill(&cfg.ExcludeCIDRs, r.excludeCIDRs, NormCIDR)
	fill(&cfg.DirectCIDRs, r.directCIDRs, NormCIDR)
	for _, c := range r.ruleCIDRs {
		if v, ok := NormCIDR(c); ok && !containsStr(cfg.ExcludeCIDRs, v) && !containsStr(cfg.DirectCIDRs, v) {
			cfg.DirectCIDRs = append(cfg.DirectCIDRs, v)
		}
	}
	return cfg, skipped
}

func containsStr(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

var domainLabelRe = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?$`)

// NormDomain lower-cases a domain suffix, strips a leading "+." / "*." /
// "." and a trailing ".", and reports whether it is a valid name.
func NormDomain(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, pre := range []string{"+.", "*.", "."} {
		s = strings.TrimPrefix(s, pre)
	}
	s = strings.TrimSuffix(s, ".")
	if s == "" || len(s) > 253 {
		return "", false
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return "", false
	}
	for _, l := range strings.Split(s, ".") {
		if len(l) > 63 || !domainLabelRe.MatchString(l) {
			return "", false
		}
	}
	return s, true
}

// NormIP checks an IP address.
func NormIP(s string) (string, bool) {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil || a.IsUnspecified() {
		return "", false
	}
	return a.Unmap().String(), true
}

// NormCIDR checks a range ("10.0.0.0/8", or a bare address taken as a
// host route) and returns it in canonical form.
func NormCIDR(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked().String(), true
	}
	if a, err := netip.ParseAddr(s); err == nil && !a.IsUnspecified() {
		a = a.Unmap()
		return netip.PrefixFrom(a, a.BitLen()).String(), true
	}
	return "", false
}
