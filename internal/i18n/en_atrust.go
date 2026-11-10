package i18n

// English for the aTrust 兼容 configuration: the reference template, the
// import/export (internal/app/atrustcfg.go) and the settings sheet
// (internal/view/settings/atrust.go).
func init() {
	for k, v := range map[string]string{
		// ---- internal/app/atrustcfg.go ----
		atrustReferenceZH: atrustReferenceEN,
		"澜流 aTrust 兼容配置。在「设置 › 代理 › aTrust 兼容 › 配置… › 导入…」中载入；各项含义见「复制参考配置」。": "Lanflow aTrust compatibility configuration. Load it with Settings > Proxy > aTrust Compatibility > Configure… > Import…; see Copy Reference Config for what each field means.",
		"读取 %s 失败：%v": "Failed to read %s: %v",
		"%s 过大（%d 字节），不像是 aTrust 配置文件": "%s is too large (%d bytes) to be an aTrust configuration file",
		"%s 不是有效的 YAML/JSON：%v":        "%s is not valid YAML/JSON: %v",
		"文件里没有可识别的 aTrust 配置。支持澜流的 YAML/JSON 格式，以及 JavaScript 覆写脚本（如 atrust-coexist.js）。": "The file holds no recognisable aTrust configuration. Lanflow's YAML/JSON format and a JavaScript override script (such as atrust-coexist.js) are supported.",
		"已从 %s 读取：内网域名 %d 个、直连域名 %d 个、公司 DNS %d 个、不进入 TUN 的网段 %d 个、仅直连的网段 %d 个。":          "Read from %s: %d intranet domains, %d direct domains, %d company DNS servers, %d ranges kept out of the TUN, %d direct-only ranges.",
		"忽略了 %d 项无法识别的条目。": " %d unrecognised entries were skipped.",

		// ---- internal/view/settings/atrust.go ----
		"第 %d 行不是有效的域名：%s":                          "Line %d is not a valid domain: %s",
		"第 %d 行不是有效的 IP 地址：%s":                      "Line %d is not a valid IP address: %s",
		"第 %d 行不是有效的网段：%s":                          "Line %d is not a valid range: %s",
		"%s 已在「内网域名」中":                              "%s is already in Intranet Domains",
		"%s 已在「不进入 TUN 的网段」中":                       "%s is already in Ranges Kept Out of the TUN",
		"%s 包含公司 DNS %s，排除后 DNS 劫持会失效；请改放到「仅直连的网段」": "%s holds the company DNS %s; excluding it breaks the DNS hijack. Move it to Direct-Only Ranges.",
		"网关 %s":             "gateways %s",
		"已配置 %d 个域名、%d 个网段": "%d domains and %d ranges configured",
		"（未运行 aTrust 时也应用）": " (applied without aTrust too)",
		"配置…":               "Configure…",
		"aTrust 兼容配置":       "aTrust Compatibility Configuration",
		"每行一项。这些列表叠加在自动检测的结果之上；澜流不内置任何公司的地址，留空即不生效。「复制参考配置」里有每一项的说明。": "One entry per line. These lists sit on top of what is detected automatically; Lanflow ships no company's addresses, and an empty list does nothing. Copy Reference Config explains every field.",
		"内网域名": "Intranet Domains",
		"内网域名后缀：直接连接、不分配 fake-ip、不嗅探，由公司 DNS 解析": "Intranet domain suffixes: direct, no fake IP, not sniffed, resolved by the company DNS",
		"直连域名": "Direct Domains",
		"需要直连但不能走公司 DNS 的公网域名（如钉钉、企业微信），由公网 DNS 解析": "Public domains that must connect directly but never use the company DNS (DingTalk, WeCom…); resolved by public DNS",
		"公司 DNS": "Company DNS",
		"公司 DNS 服务器的 IP；留空则使用检测到的 aTrust 解析器": "IP addresses of the company DNS servers; empty uses the detected aTrust resolvers",
		"不进入 TUN 的网段": "Ranges Kept Out of the TUN",
		"交给系统路由（aTrust）处理并直接连接；切勿放入含 DNS 服务器的网段": "Left to the system routes (aTrust) and direct; never put a range holding a DNS server here",
		"仅直连的网段": "Direct-Only Ranges",
		"直接连接但仍经过 TUN，发往其中 DNS 的查询照常被接管（例如含公司 DNS 的 10.0.0.0/8）": "Direct, but still through the TUN, so DNS queries to servers inside are still captured (e.g. 10.0.0.0/8 holding the company DNS)",
		"未运行 aTrust 时也应用":                                                 "Apply Without aTrust Too",
		"在公司办公网不开 aTrust 时，内网域名仍需公司 DNS 解析":                               "On the office network without aTrust, intranet names still need the company DNS",
		"读取澜流的 YAML/JSON 或 JavaScript 覆写脚本（如 atrust-coexist.js），填入表单后再保存": "Reads Lanflow's YAML/JSON or a JavaScript override script (such as atrust-coexist.js) into the form; save afterwards",
		"复制参考配置": "Copy Reference Config",
		"参考配置已复制，粘贴到文本编辑器即可查看每一项的说明": "Reference config copied; paste it into a text editor to read what each field means",
		"导出参考配置…":        "Export Reference Config…",
		"aTrust 兼容配置已保存": "aTrust compatibility configuration saved",
		"aTrust 配置":      "aTrust configuration",
		"导入 aTrust 配置":   "Import aTrust Configuration",
		"选择澜流导出的 aTrust 配置（YAML/JSON），或 JavaScript 覆写脚本（如 atrust-coexist.js）。内容会填入表单，确认后再保存。": "Choose an aTrust configuration exported by Lanflow (YAML/JSON) or a JavaScript override script (such as atrust-coexist.js). It fills the form; save once reviewed.",
		"导出 aTrust 配置":   "Export aTrust Configuration",
		"导出 aTrust 参考配置": "Export aTrust Reference Config",
	} {
		en[k] = v
	}
}

// atrustReferenceZH must stay identical to atrustReference in
// internal/app/atrustcfg.go (the catalog test checks the key is used).
const atrustReferenceZH = `# 澜流 · aTrust 兼容配置（参考模板）
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

const atrustReferenceEN = `# Lanflow - aTrust compatibility configuration (reference template)
# Usage: fill the lists in Settings > Proxy > aTrust Compatibility > Configure…, one entry per line;
# or edit this file, load it with Import… and save once reviewed.
# Every value below is an example (corp.example.com, 10.x, 203.0.113.x): replace them with your company's.
# Lanflow ships no company's addresses: an empty field does nothing and automatic detection goes on as usual.
#
# Detection and this configuration add up: while aTrust runs, the ranges, domains, DNS servers and gateways
# Lanflow reads from its routes and resolvers join automatically. Put here what cannot be detected,
# e.g. domains that need the company DNS while aTrust is not running.

# Apply these lists while aTrust is not running too. On the office network without aTrust,
# intranet names still need the company DNS to resolve.
always: false

# Intranet domain suffixes: direct, no fake IP, not sniffed, resolved by corp-dns.
# Only true intranet names; domains public DNS resolves the same way belong in direct-domains.
corp-domains:
  - corp.example.com
  - intranet.example.net

# Public domains that must connect directly but never use the company DNS (SaaS such as DingTalk, WeCom).
# Away from the office the company DNS is only reachable through aTrust, queries time out and these
# sites all fail; they are resolved by public DNS (plain UDP).
direct-domains:
  - chat.example.org
  - docs.example.org

# Company DNS servers (IP addresses). Empty: the detected aTrust resolvers are used.
corp-dns:
  - 10.1.0.53
  - 10.1.0.54

# Ranges kept out of the TUN: written to tun.route-exclude-address, left to the system routes (aTrust), and direct.
# Put here: aTrust's mapped range (usually 198.18.0.0/16; exclude the /16 only, never a /15), its gateways as /32,
# intranet ranges reachable only through aTrust.
# Never put a range holding any DNS server here: with the company DNS at 10.1.0.53, do not exclude 10.0.0.0/8.
# DNS queries sent into an excluded range bypass the core, the DNS hijack stops working and every blocked site
# fails. Such ranges belong in direct-cidrs.
# While aTrust runs its gateways are detected and excluded automatically; a detected range holding a DNS server
# is demoted to direct-only automatically as well.
exclude-cidrs:
  - 198.18.0.0/16
  - 172.16.0.0/12
  - 203.0.113.10/32

# Direct-only ranges: direct, but still through the TUN, so DNS queries to servers inside are captured as usual.
direct-cidrs:
  - 10.0.0.0/8
`
