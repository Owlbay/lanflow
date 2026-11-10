package profile

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"lanflow/internal/netx"
)

// maxBody is the largest subscription body we accept.
const maxBody = 32 << 20

func fetch(ctx context.Context, client *http.Client, rawURL, userAgent string) (Fetched, error) {
	if client == nil {
		client = netx.Direct(30 * time.Second)
	}
	if userAgent == "" {
		userAgent = netx.UserAgent
	}
	if ctx == nil {
		ctx = context.Background()
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Fetched{}, errors.New("订阅链接无效：请填写以 http:// 或 https:// 开头的完整地址")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Fetched{}, errors.New("订阅链接无效：" + sanitizeErr(err, u))
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	if err != nil {
		return Fetched{}, transportError(err, u)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Fetched{}, statusError(resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return Fetched{}, transportError(err, u)
	}
	if len(body) > maxBody {
		return Fetched{}, errors.New("下载失败：订阅内容超过 32 MB，不像是配置文件，请检查订阅链接")
	}

	f := Fetched{Data: body}
	f.Name = parseDispositionName(resp.Header.Get("Content-Disposition"))
	f.Upload, f.Download, f.Total, f.Expire = parseUserInfo(resp.Header.Get("Subscription-Userinfo"))
	if v := strings.TrimSpace(resp.Header.Get("Profile-Update-Interval")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			f.UpdateInterval = n
		}
	}
	if v := strings.TrimSpace(resp.Header.Get("Profile-Web-Page-Url")); v != "" {
		if hu, err := url.Parse(v); err == nil && (hu.Scheme == "http" || hu.Scheme == "https") && hu.Host != "" {
			f.HomePage = hu.String()
		}
	}

	if err := checkYAML(body); err != nil {
		if looksLikeShareLinks(body) {
			return Fetched{}, errors.New("订阅内容不是 Clash/Mihomo 格式的订阅：服务商返回的是节点分享链接（Base64 / ss:// 等）。澜流不做格式转换，请在服务商处选择 Clash 或 Mihomo 格式的订阅链接，或尝试更换 User-Agent")
		}
		return Fetched{}, err
	}
	return f, nil
}

// sanitizeErr renders err without the URL (which may carry a token).
func sanitizeErr(err error, u *url.URL) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := err.Error()
	if u != nil {
		msg = strings.ReplaceAll(msg, u.String(), u.Scheme+"://"+u.Host+"/…")
		if u.RawQuery != "" {
			msg = strings.ReplaceAll(msg, u.RawQuery, "…")
		}
	}
	return msg
}

func transportError(err error, u *url.URL) error {
	if errors.Is(err, context.Canceled) {
		return errors.New("下载已取消")
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) ||
		strings.Contains(err.Error(), "Client.Timeout") {
		return errors.New("下载超时：服务器长时间没有响应。请检查网络，若订阅需要代理才能访问，请勾选「通过代理下载」后重试")
	}
	msg := sanitizeErr(err, u)
	switch {
	case strings.Contains(msg, "no such host"):
		return fmt.Errorf("下载失败：无法解析域名 %s，请检查订阅链接或网络", u.Hostname())
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("下载失败：%s 拒绝连接，请稍后重试", u.Host)
	case strings.Contains(msg, "certificate") || strings.Contains(msg, "x509") || strings.Contains(msg, "tls:"):
		return fmt.Errorf("下载失败：%s 的 TLS 证书无效（%s）", u.Host, msg)
	}
	return fmt.Errorf("下载失败：无法连接到 %s（%s）", u.Host, msg)
}

func statusError(code int) error {
	var hint string
	switch {
	case code == 401 || code == 403:
		hint = "链接可能已失效、被服务商拒绝，或需要特定的 User-Agent，请到服务商处重新复制订阅链接"
	case code == 404 || code == 410:
		hint = "链接不存在或已失效，请到服务商处重新复制订阅链接"
	case code == 429:
		hint = "请求过于频繁，请稍后再试"
	case code >= 500:
		hint = "服务商服务器出错，请稍后再试"
	case code >= 300 && code < 400:
		hint = "服务器要求跳转但未能跟随，请检查订阅链接"
	default:
		hint = "请检查订阅链接是否正确"
	}
	return fmt.Errorf("下载失败：HTTP %d。%s", code, hint)
}

// parseUserInfo parses "upload=1; download=2; total=3; expire=1700000000".
func parseUserInfo(h string) (up, down, total int64, expire time.Time) {
	for _, part := range strings.Split(h, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			continue
		}
		n := int64(f)
		switch k {
		case "upload":
			up = n
		case "download":
			down = n
		case "total":
			total = n
		case "expire":
			if n > 0 {
				expire = time.Unix(n, 0)
			}
		}
	}
	return
}

var (
	reFilenameStar = regexp.MustCompile(`(?i)filename\*\s*=\s*(?:utf-8|iso-8859-1)?'[^']*'([^;]+)`)
	reFilename     = regexp.MustCompile(`(?i)filename\s*=\s*("([^"]*)"|'([^']*)'|([^;]+))`)
)

// parseDispositionName extracts the file name from a Content-Disposition
// header, preferring the RFC 5987 filename* form, URL-decoding it and
// stripping a .yaml/.yml extension.
func parseDispositionName(h string) string {
	h = strings.TrimSpace(h)
	if h == "" {
		return ""
	}
	var name string
	if _, params, err := mime.ParseMediaType(h); err == nil {
		name = params["filename"]
	}
	if name == "" {
		if m := reFilenameStar.FindStringSubmatch(h); m != nil {
			name = m[1]
		} else if m := reFilename.FindStringSubmatch(h); m != nil {
			name = m[2] + m[3] + m[4]
		}
	}
	name = strings.TrimSpace(name)
	if strings.Contains(name, "%") {
		if dec, err := url.PathUnescape(name); err == nil {
			name = dec
		} else if dec, err := url.QueryUnescape(name); err == nil {
			name = dec
		}
	}
	name = strings.Trim(name, `"' `)
	lower := strings.ToLower(name)
	for _, ext := range []string{".yaml", ".yml"} {
		if strings.HasSuffix(lower, ext) {
			name = name[:len(name)-len(ext)]
			break
		}
	}
	// Never return a path.
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	return strings.TrimSpace(name)
}

var shareSchemes = []string{"ss://", "ssr://", "vmess://", "vless://", "trojan://", "hysteria://",
	"hysteria2://", "hy2://", "tuic://", "wireguard://", "wg://", "socks://", "socks5://", "http://", "https://", "anytls://"}

func isShareLinkList(text string) bool {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	n := 0
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		ok := false
		for _, s := range shareSchemes {
			if strings.HasPrefix(strings.ToLower(l), s) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
		n++
	}
	return n > 0
}

// looksLikeShareLinks reports whether body is a list of node share links,
// raw or base64-encoded.
func looksLikeShareLinks(body []byte) bool {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return false
	}
	if isShareLinkList(text) {
		return true
	}
	compact := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, text)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if dec, err := enc.DecodeString(compact); err == nil && len(dec) > 0 {
			if isShareLinkList(string(dec)) {
				return true
			}
		}
	}
	return false
}

var reYAMLLine = regexp.MustCompile(`line (\d+)`)

// yamlError turns a yaml.v3 error into a Chinese message keeping the line.
func yamlError(err error) string {
	msg := strings.TrimPrefix(err.Error(), "yaml: ")
	if m := reYAMLLine.FindStringSubmatch(msg); m != nil {
		return fmt.Sprintf("第 %s 行 YAML 语法错误：%s", m[1], strings.TrimSpace(strings.TrimPrefix(msg, "line "+m[1]+":")))
	}
	return "YAML 语法错误：" + msg
}

// parseDoc parses YAML into its root content node and checks it is a mapping.
// Empty input yields an empty mapping.
func parseDoc(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, errors.New(yamlError(err))
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("顶层必须是键值映射（key: value），而不是列表或纯文本")
	}
	return root, nil
}

func checkYAML(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return errors.New("订阅内容为空，不是 Clash/Mihomo 格式的订阅")
	}
	root, err := parseDoc(data)
	if err != nil {
		return fmt.Errorf("订阅内容不是 Clash/Mihomo 格式的订阅：%s", err.Error())
	}
	for _, key := range []string{"proxies", "proxy-providers"} {
		v := mapGet(root, key)
		if v == nil {
			continue
		}
		v = deref(v)
		if (v.Kind == yaml.SequenceNode || v.Kind == yaml.MappingNode) && len(v.Content) > 0 {
			return nil
		}
	}
	return errors.New("订阅内容不是 Clash/Mihomo 格式的订阅：缺少 proxies 或 proxy-providers。请确认服务商提供的是 Clash/Mihomo 配置链接")
}
