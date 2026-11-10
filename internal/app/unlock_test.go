package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"lanflow/internal/model"
)

// hostRT sends every request to the test server, keeping the original
// host and path in the request so the handler can play the real sites.
type hostRT struct{ target string }

func (r hostRT) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := url.Parse(r.target)
	if err != nil {
		return nil, err
	}
	req2 := req.Clone(req.Context())
	req2.Header.Set("X-Orig-Host", req.URL.Host)
	req2.URL.Scheme, req2.URL.Host = u.Scheme, u.Host
	return http.DefaultTransport.RoundTrip(req2)
}

// siteServer plays the sites: handler gets the original host and path.
func siteServer(t *testing.T, handler func(w http.ResponseWriter, host, path string, r *http.Request)) *http.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(w, r.Header.Get("X-Orig-Host"), r.URL.Path, r)
	}))
	t.Cleanup(srv.Close)
	return &http.Client{Transport: hostRT{target: srv.URL}, Timeout: 5 * time.Second}
}

func byName(results []model.UnlockResult, name string) model.UnlockResult {
	for _, r := range results {
		if r.Service == name {
			return r
		}
	}
	return model.UnlockResult{}
}

// A Hong Kong exit that every service accepts: the verdicts and regions
// come from the status codes, redirects and page markers.
func TestUnlockChecksAllOpen(t *testing.T) {
	client := siteServer(t, func(w http.ResponseWriter, host, path string, r *http.Request) {
		switch {
		case host == "api.openai.com":
			_, _ = w.Write([]byte(`{"cookie_requirements":"none"}`))
		case host == "ios.chat.openai.com":
			_, _ = w.Write([]byte("ok"))
		case strings.HasSuffix(path, "/cdn-cgi/trace"):
			_, _ = w.Write([]byte("ip=1.2.3.4\nloc=HK\ncolo=HKG\n"))
		case host == "claude.ai":
			_, _ = w.Write([]byte("<html>Claude</html>"))
		case host == "gemini.google.com":
			_, _ = w.Write([]byte(`stuff ,2,1,200,"HKG" more`))
		case host == "www.netflix.com":
			w.Header().Set("X-Originating-URL", "https://www.netflix.com/hk-en"+path)
			_, _ = w.Write([]byte("title"))
		case host == "www.youtube.com":
			_, _ = w.Write([]byte(`YouTube Premium "countryCode":"HK" ad-free`))
		case host == "www.disneyplus.com" && path == "/":
			http.Redirect(w, r, "/en-hk/", http.StatusFound)
		case host == "www.disneyplus.com":
			_, _ = w.Write([]byte("Disney+"))
		case host == "www.tiktok.com":
			_, _ = w.Write([]byte(`{"region":"HK"}`))
		case host == "www.spotify.com" && path == "/":
			http.Redirect(w, r, "/hk-en/", http.StatusFound)
		case host == "www.spotify.com":
			_, _ = w.Write([]byte("Spotify"))
		case host == "www.google.com":
			_, _ = w.Write([]byte("results"))
		case host == "www.bing.com":
			w.Header().Add("Set-Cookie", "_EDGE_S=F=1&SID=abc&mkt=zh-hk; path=/")
			_, _ = w.Write([]byte("bing"))
		default:
			http.NotFound(w, r)
		}
	})
	var mu sync.Mutex
	arrived := 0
	results := runUnlockChecks(context.Background(), client, unlockChecks, func(int, model.UnlockResult) {
		mu.Lock()
		arrived++
		mu.Unlock()
	})
	if len(results) != len(unlockChecks) || arrived != len(unlockChecks) {
		t.Fatalf("%d results, %d callbacks", len(results), arrived)
	}
	for i, r := range results {
		if r.Service != unlockChecks[i].Name {
			t.Errorf("result %d is %s, want %s (table order)", i, r.Service, unlockChecks[i].Name)
		}
		if r.Status != model.UnlockYes {
			t.Errorf("%s: %+v", r.Service, r)
		}
		if r.Millis <= 0 {
			t.Errorf("%s: no timing", r.Service)
		}
	}
	for name, region := range map[string]string{"ChatGPT": "HK", "Claude": "HK", "Gemini": "HKG", "Netflix": "HK", "YouTube Premium": "HK", "Disney+": "HK", "TikTok": "HK", "Spotify": "HK", "Bing": "HK", "Google 搜索": ""} {
		if got := byName(results, name).Region; got != region {
			t.Errorf("%s region = %q, want %q", name, got, region)
		}
	}
	if byName(results, "Netflix").Note != "完整解锁" {
		t.Errorf("Netflix note = %q", byName(results, "Netflix").Note)
	}
}

// A blocked exit: every refusal the sites use maps to 不可用 with a reason;
// Netflix with only the original playable is 仅自制剧.
func TestUnlockChecksBlocked(t *testing.T) {
	client := siteServer(t, func(w http.ResponseWriter, host, path string, r *http.Request) {
		switch {
		case host == "api.openai.com":
			_, _ = w.Write([]byte(`{"error":"unsupported_country"}`))
		case strings.HasSuffix(path, "/cdn-cgi/trace"):
			_, _ = w.Write([]byte("ip=1.2.3.4\nloc=CN\n"))
		case host == "claude.ai":
			w.WriteHeader(http.StatusForbidden)
		case host == "gemini.google.com" && path == "/":
			http.Redirect(w, r, "https://gemini.google.com/unsupported", http.StatusFound)
		case host == "www.netflix.com" && path == "/title/81280792":
			_, _ = w.Write([]byte("original"))
		case host == "www.netflix.com":
			http.NotFound(w, r)
		case host == "www.youtube.com":
			_, _ = w.Write([]byte(`Premium is not available in your country "countryCode":"CN"`))
		case host == "www.disneyplus.com" && path == "/":
			http.Redirect(w, r, "https://www.disneyplus.com/unavailable", http.StatusFound)
		case host == "www.disneyplus.com":
			_, _ = w.Write([]byte("Disney+ is not available"))
		case host == "www.tiktok.com":
			w.WriteHeader(http.StatusForbidden)
		case host == "www.spotify.com" && path == "/":
			http.Redirect(w, r, "/int/", http.StatusFound)
		case host == "www.spotify.com":
			_, _ = w.Write([]byte("Spotify"))
		case host == "www.google.com":
			w.Header().Set("Location", "https://www.google.com/sorry/index?continue=x")
			w.WriteHeader(http.StatusFound)
		case host == "www.bing.com":
			w.Header().Set("Location", "https://cn.bing.com/")
			w.WriteHeader(http.StatusFound)
		case host == "gemini.google.com":
			_, _ = w.Write([]byte("unsupported"))
		default:
			http.NotFound(w, r)
		}
	})
	results := runUnlockChecks(context.Background(), client, unlockChecks, nil)
	want := map[string]string{
		"ChatGPT": model.UnlockNo, "Claude": model.UnlockNo, "Gemini": model.UnlockNo, "Netflix": model.UnlockPartial,
		"YouTube Premium": model.UnlockNo, "Disney+": model.UnlockNo, "TikTok": model.UnlockNo, "Spotify": model.UnlockNo,
		"Google 搜索": model.UnlockNo, "Bing": model.UnlockYes,
	}
	for name, status := range want {
		if r := byName(results, name); r.Status != status {
			t.Errorf("%s = %+v, want %s", name, r, status)
		}
	}
	if r := byName(results, "ChatGPT"); r.Region != "CN" || r.Note != "所在地区不支持" {
		t.Errorf("ChatGPT = %+v", r)
	}
	if r := byName(results, "Netflix"); r.Note != "仅自制剧" || r.Region != "US" {
		t.Errorf("Netflix = %+v", r)
	}
	if r := byName(results, "Google 搜索"); !strings.Contains(r.Note, "人机验证") {
		t.Errorf("Google = %+v", r)
	}
	if r := byName(results, "Bing"); r.Region != "CN" {
		t.Errorf("Bing = %+v", r)
	}
}

// A dead exit: the checks report 失败 rather than hanging, within the
// per-check timeout.
func TestUnlockChecksUnreachable(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	results := runUnlockChecks(context.Background(), client, unlockChecks, nil)
	for _, r := range results {
		if r.Status != model.UnlockError || r.Note != "超时" {
			t.Errorf("%s = %+v", r.Service, r)
		}
	}
}

func TestPathHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"https://www.spotify.com/hk-en/": "HK", "https://www.spotify.com/us/": "US", "https://www.spotify.com/int/": "",
	} {
		if got := pathRegion(in, true); got != want {
			t.Errorf("pathRegion(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"https://www.disneyplus.com/en-gb/": "GB", "https://www.disneyplus.com/zh-hant-tw/": "TW", "https://www.disneyplus.com/": "",
	} {
		if got := pathRegion(in, false); got != want {
			t.Errorf("pathRegion(%q) = %q, want %q", in, got, want)
		}
	}
	if got := netflixRegion(unlockResp{Status: 200, FinalURL: "https://www.netflix.com/title/81280792"}); got != "US" {
		t.Errorf("netflix US = %q", got)
	}
	if got := netflixRegion(unlockResp{Status: 200, FinalURL: "https://www.netflix.com/jp-en/title/81280792"}); got != "JP" {
		t.Errorf("netflix JP = %q", got)
	}
	if got := traceLoc(unlockResp{Status: 200, Body: "fl=1\nip=1.1.1.1\nloc=jp\n"}); got != "JP" {
		t.Errorf("traceLoc = %q", got)
	}
}
