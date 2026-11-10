package app

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// 解锁检测 (probe.go): can an exit use the streaming and AI services
// people switch nodes for? Every service is one entry of unlockChecks: the
// requests to make through the exit and a judge that turns the responses
// into a verdict. The checks are plain HTTP (status codes, redirects, the
// region markers the sites put in their pages); nothing logs in and no
// account is touched. They run concurrently with short timeouts and only
// when the user asks (probe.go).

const (
	unlockTimeout   = 10 * time.Second
	unlockBodyMax   = 256 << 10
	unlockBrowserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

// unlockReq is one request of a check.
type unlockReq struct {
	Method     string // "" = GET
	URL        string
	Headers    map[string]string
	Body       string
	NoRedirect bool // stop at the first response (read Location yourself)
}

// unlockResp is what a request got back; Err set means no response.
type unlockResp struct {
	Status   int
	Header   http.Header
	Body     string
	FinalURL string // after redirects (the request URL with NoRedirect)
	Err      error
}

// unlockCheck is one service: the requests, made in order, and the judge.
// The judge sees one unlockResp per request and fills Status, Region and
// Note; Service and Millis are added by runUnlockChecks.
type unlockCheck struct {
	Name  string
	Reqs  []unlockReq
	Judge func(rs []unlockResp) model.UnlockResult
}

var (
	reCountryCode  = regexp.MustCompile(`"countryCode"\s*:\s*"([A-Za-z]{2})"`)
	reTikTokRegion = regexp.MustCompile(`"region"\s*:\s*"([A-Za-z]{2})"`)
	reGeminiRegion = regexp.MustCompile(`,2,1,200,"([A-Z]{3})"`)
	reBingMkt      = regexp.MustCompile(`mkt=([a-zA-Z]{2})-([a-zA-Z]{2})`)
)

// unlockChecks is the service table, in display order.
var unlockChecks = []unlockCheck{
	{
		Name: "ChatGPT",
		Reqs: []unlockReq{
			{URL: "https://api.openai.com/compliance/cookie_requirements", Headers: map[string]string{"User-Agent": unlockBrowserUA}},
			{URL: "https://ios.chat.openai.com/", Headers: map[string]string{"User-Agent": unlockBrowserUA}},
			{URL: "https://chatgpt.com/cdn-cgi/trace"},
		},
		Judge: func(rs []unlockResp) model.UnlockResult {
			if rs[0].Err != nil {
				return unlockErr(rs[0].Err)
			}
			region := traceLoc(rs[2])
			if strings.Contains(rs[0].Body, "unsupported_country") {
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.T("所在地区不支持")}
			}
			if rs[1].Err == nil && strings.Contains(rs[1].Body, "VPN") {
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.T("被识别为 VPN，需更换节点")}
			}
			if rs[0].Status == http.StatusForbidden {
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.T("访问被拒绝（HTTP 403）")}
			}
			return model.UnlockResult{Status: model.UnlockYes, Region: region}
		},
	},
	{
		Name: "Claude",
		Reqs: []unlockReq{
			{URL: "https://claude.ai/", Headers: map[string]string{"User-Agent": unlockBrowserUA}},
			{URL: "https://claude.ai/cdn-cgi/trace"},
		},
		Judge: func(rs []unlockResp) model.UnlockResult {
			if rs[0].Err != nil {
				return unlockErr(rs[0].Err)
			}
			region := traceLoc(rs[1])
			switch {
			case rs[0].Status == http.StatusForbidden:
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.T("访问被拒绝（HTTP 403），地区或 IP 不受支持")}
			case strings.Contains(rs[0].FinalURL, "unavailable") || strings.Contains(rs[0].Body, "app-unavailable-in-region"):
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.T("所在地区不支持")}
			case rs[0].Status != http.StatusOK:
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.Tf("HTTP %d", rs[0].Status)}
			}
			return model.UnlockResult{Status: model.UnlockYes, Region: region}
		},
	},
	{
		Name: "Gemini",
		Reqs: []unlockReq{{URL: "https://gemini.google.com/", Headers: map[string]string{"User-Agent": unlockBrowserUA}}},
		Judge: func(rs []unlockResp) model.UnlockResult {
			if rs[0].Err != nil {
				return unlockErr(rs[0].Err)
			}
			region := ""
			if m := reGeminiRegion.FindStringSubmatch(rs[0].Body); m != nil {
				region = m[1]
			}
			switch {
			case strings.Contains(rs[0].FinalURL, "unsupported") || strings.Contains(rs[0].Body, "gemini.google.com/unsupported"):
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.T("所在地区不支持")}
			case rs[0].Status != http.StatusOK:
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.Tf("HTTP %d", rs[0].Status)}
			}
			return model.UnlockResult{Status: model.UnlockYes, Region: region}
		},
	},
	{
		Name: "Netflix",
		Reqs: []unlockReq{
			{URL: "https://www.netflix.com/title/81280792", Headers: map[string]string{"User-Agent": unlockBrowserUA}}, // an original
			{URL: "https://www.netflix.com/title/70143836", Headers: map[string]string{"User-Agent": unlockBrowserUA}}, // licensed content
		},
		Judge: func(rs []unlockResp) model.UnlockResult {
			if rs[0].Err != nil {
				return unlockErr(rs[0].Err)
			}
			region := netflixRegion(rs[0])
			switch {
			case rs[0].Status == http.StatusForbidden:
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.T("Netflix 拒绝了这个 IP（HTTP 403）")}
			case rs[0].Status == http.StatusNotFound:
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.T("无法观看任何影片")}
			case rs[0].Status != http.StatusOK:
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.Tf("HTTP %d", rs[0].Status)}
			case rs[1].Err == nil && rs[1].Status == http.StatusOK:
				return model.UnlockResult{Status: model.UnlockYes, Region: region, Note: i18n.T("完整解锁")}
			}
			return model.UnlockResult{Status: model.UnlockPartial, Region: region, Note: i18n.T("仅自制剧")}
		},
	},
	{
		Name: "YouTube Premium",
		Reqs: []unlockReq{{URL: "https://www.youtube.com/premium", Headers: map[string]string{
			"User-Agent": unlockBrowserUA, "Accept-Language": "en", "Cookie": "YSC=lanflow; CONSENT=YES+cb",
		}}},
		Judge: func(rs []unlockResp) model.UnlockResult {
			if rs[0].Err != nil {
				return unlockErr(rs[0].Err)
			}
			region := ""
			if m := reCountryCode.FindStringSubmatch(rs[0].Body); m != nil {
				region = strings.ToUpper(m[1])
			}
			switch {
			case strings.Contains(rs[0].FinalURL, "google.cn"):
				return model.UnlockResult{Status: model.UnlockNo, Region: "CN", Note: i18n.T("跳转到了 google.cn")}
			case strings.Contains(rs[0].Body, "Premium is not available in your country"):
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.T("所在地区没有 Premium")}
			case rs[0].Status != http.StatusOK:
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.Tf("HTTP %d", rs[0].Status)}
			}
			return model.UnlockResult{Status: model.UnlockYes, Region: region}
		},
	},
	{
		Name: "Disney+",
		Reqs: []unlockReq{{URL: "https://www.disneyplus.com/", Headers: map[string]string{"User-Agent": unlockBrowserUA}}},
		Judge: func(rs []unlockResp) model.UnlockResult {
			if rs[0].Err != nil {
				return unlockErr(rs[0].Err)
			}
			region := pathRegion(rs[0].FinalURL, false)
			switch {
			case rs[0].Status == http.StatusForbidden:
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.T("访问被拒绝（HTTP 403）")}
			case strings.Contains(rs[0].FinalURL, "unavailable") || strings.Contains(rs[0].FinalURL, "/preview"):
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.T("所在地区尚未开通")}
			case rs[0].Status != http.StatusOK:
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.Tf("HTTP %d", rs[0].Status)}
			}
			return model.UnlockResult{Status: model.UnlockYes, Region: region}
		},
	},
	{
		Name: "TikTok",
		Reqs: []unlockReq{{URL: "https://www.tiktok.com/", Headers: map[string]string{"User-Agent": unlockBrowserUA}}},
		Judge: func(rs []unlockResp) model.UnlockResult {
			if rs[0].Err != nil {
				return unlockErr(rs[0].Err)
			}
			region := ""
			if m := reTikTokRegion.FindStringSubmatch(rs[0].Body); m != nil {
				region = strings.ToUpper(m[1])
			}
			switch {
			case rs[0].Status == http.StatusForbidden:
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.T("访问被拒绝（HTTP 403）")}
			case rs[0].Status != http.StatusOK:
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.Tf("HTTP %d", rs[0].Status)}
			case region == "":
				return model.UnlockResult{Status: model.UnlockYes, Note: i18n.T("页面中没有地区标记")}
			}
			return model.UnlockResult{Status: model.UnlockYes, Region: region}
		},
	},
	{
		Name: "Spotify",
		Reqs: []unlockReq{{URL: "https://www.spotify.com/", Headers: map[string]string{"User-Agent": unlockBrowserUA}}},
		Judge: func(rs []unlockResp) model.UnlockResult {
			if rs[0].Err != nil {
				return unlockErr(rs[0].Err)
			}
			seg := firstPathSegment(rs[0].FinalURL)
			region := pathRegion(rs[0].FinalURL, true)
			switch {
			case seg == "int":
				return model.UnlockResult{Status: model.UnlockNo, Note: i18n.T("所在地区尚未开通")}
			case rs[0].Status != http.StatusOK:
				return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.Tf("HTTP %d", rs[0].Status)}
			}
			return model.UnlockResult{Status: model.UnlockYes, Region: region}
		},
	},
	{
		Name: "Google 搜索",
		Reqs: []unlockReq{{URL: "https://www.google.com/search?q=lanflow", Headers: map[string]string{"User-Agent": unlockBrowserUA, "Accept-Language": "en"}, NoRedirect: true}},
		Judge: func(rs []unlockResp) model.UnlockResult {
			if rs[0].Err != nil {
				return unlockErr(rs[0].Err)
			}
			loc := rs[0].Header.Get("Location")
			switch {
			case rs[0].Status == http.StatusTooManyRequests || strings.Contains(loc, "/sorry/"):
				return model.UnlockResult{Status: model.UnlockNo, Note: i18n.T("要求人机验证：这个 IP 的信誉偏低")}
			case strings.Contains(loc, "consent.google.com"):
				return model.UnlockResult{Status: model.UnlockYes, Region: "EU", Note: i18n.T("先要同意 Cookie 提示（欧盟地区）")}
			case strings.Contains(loc, "google.cn"):
				return model.UnlockResult{Status: model.UnlockNo, Region: "CN", Note: i18n.T("跳转到了 google.cn")}
			case rs[0].Status == http.StatusOK:
				return model.UnlockResult{Status: model.UnlockYes}
			case rs[0].Status/100 == 3:
				return model.UnlockResult{Status: model.UnlockYes, Note: i18n.Tf("跳转到 %s", loc)}
			}
			return model.UnlockResult{Status: model.UnlockNo, Note: i18n.Tf("HTTP %d", rs[0].Status)}
		},
	},
	{
		Name: "Bing",
		Reqs: []unlockReq{{URL: "https://www.bing.com/", Headers: map[string]string{"User-Agent": unlockBrowserUA}, NoRedirect: true}},
		Judge: func(rs []unlockResp) model.UnlockResult {
			if rs[0].Err != nil {
				return unlockErr(rs[0].Err)
			}
			loc := rs[0].Header.Get("Location")
			region := ""
			for _, ck := range rs[0].Header.Values("Set-Cookie") {
				if m := reBingMkt.FindStringSubmatch(ck); m != nil {
					region = strings.ToUpper(m[2])
					break
				}
			}
			switch {
			case strings.Contains(loc, "cn.bing.com"):
				return model.UnlockResult{Status: model.UnlockYes, Region: "CN", Note: i18n.T("跳转到国内版 cn.bing.com")}
			case rs[0].Status == http.StatusOK || rs[0].Status/100 == 3:
				return model.UnlockResult{Status: model.UnlockYes, Region: region, Note: i18n.T("国际版")}
			}
			return model.UnlockResult{Status: model.UnlockNo, Region: region, Note: i18n.Tf("HTTP %d", rs[0].Status)}
		},
	},
}

// unlockErr is the verdict when the request itself failed.
func unlockErr(err error) model.UnlockResult {
	note := i18n.T("连接失败")
	if err != nil && (strings.Contains(err.Error(), "deadline") || strings.Contains(err.Error(), "timeout")) {
		note = i18n.T("超时")
	}
	return model.UnlockResult{Status: model.UnlockError, Note: note}
}

// traceLoc reads the loc= line of a Cloudflare trace response.
func traceLoc(r unlockResp) string {
	if r.Err != nil || r.Status != http.StatusOK {
		return ""
	}
	for _, line := range strings.Split(r.Body, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k == "loc" {
			return strings.ToUpper(strings.TrimSpace(v))
		}
	}
	return ""
}

// firstPathSegment is the first path element of u ("hk-en" for
// https://www.spotify.com/hk-en/), "" when the path is empty.
func firstPathSegment(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexByte(u, '/'); i >= 0 {
		u = u[i+1:]
	} else {
		return ""
	}
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	return strings.ToLower(u)
}

// pathRegion reads the country of a localized path such as /hk-en/ (the
// country first, as Spotify writes it) or /en-gb/ and /zh-hant-tw/ (the
// language first, as Disney+ writes it).
func pathRegion(u string, countryFirst bool) string {
	seg := firstPathSegment(u)
	if seg == "" || seg == "int" {
		return ""
	}
	parts := strings.Split(seg, "-")
	pick := parts[len(parts)-1]
	if countryFirst {
		pick = parts[0]
	}
	if len(pick) == 2 && isAlpha(pick) {
		return strings.ToUpper(pick)
	}
	return ""
}

// netflixRegion reads the country from the localized URL Netflix lands
// on (https://www.netflix.com/hk-en/title/…, /title/ alone being the US).
func netflixRegion(r unlockResp) string {
	if r.Err != nil || (r.Status != http.StatusOK && r.Status != http.StatusNotFound) {
		return ""
	}
	u := r.Header.Get("X-Originating-URL")
	if u == "" {
		u = r.FinalURL
	}
	seg := firstPathSegment(u)
	if seg == "" || seg == "title" {
		return "US"
	}
	cc := strings.SplitN(seg, "-", 2)[0]
	if len(cc) == 2 && isAlpha(cc) {
		return strings.ToUpper(cc)
	}
	return ""
}

func isAlpha(s string) bool {
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return true
}

// doUnlockReq makes one request with client (cloned to stop at redirects
// when asked) and reads a bounded body.
func doUnlockReq(ctx context.Context, client *http.Client, r unlockReq) unlockResp {
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if r.Body != "" {
		body = strings.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.URL, body)
	if err != nil {
		return unlockResp{Err: err, FinalURL: r.URL}
	}
	req.Header.Set("User-Agent", "Lanflow/0.1 unlock-check")
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	c := *client
	if r.NoRedirect {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	resp, err := c.Do(req)
	if err != nil {
		return unlockResp{Err: err, FinalURL: r.URL}
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, unlockBodyMax))
	final := r.URL
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	return unlockResp{Status: resp.StatusCode, Header: resp.Header, Body: string(data), FinalURL: final}
}

// runUnlockChecks runs checks concurrently through client and returns the
// verdicts in table order; onResult, when set, is called as each lands
// (from several goroutines).
func runUnlockChecks(ctx context.Context, client *http.Client, checks []unlockCheck, onResult func(i int, r model.UnlockResult)) []model.UnlockResult {
	out := make([]model.UnlockResult, len(checks))
	var wg sync.WaitGroup
	for i, ck := range checks {
		out[i] = model.UnlockResult{Service: ck.Name, Status: model.UnlockPending}
		wg.Add(1)
		go func(i int, ck unlockCheck) {
			defer wg.Done()
			start := time.Now()
			cctx, cancel := context.WithTimeout(ctx, unlockTimeout)
			defer cancel()
			rs := make([]unlockResp, len(ck.Reqs))
			for j, r := range ck.Reqs {
				rs[j] = doUnlockReq(cctx, client, r)
			}
			res := ck.Judge(rs)
			res.Service = ck.Name
			res.Millis = int(time.Since(start) / time.Millisecond)
			if res.Millis == 0 {
				res.Millis = 1
			}
			out[i] = res
			if onResult != nil {
				onResult(i, res)
			}
		}(i, ck)
	}
	wg.Wait()
	return out
}
