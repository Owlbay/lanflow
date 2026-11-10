package profile

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"lanflow/internal/netx"
)

const validSub = "proxies:\n  - {name: a, type: ss, server: 1.1.1.1, port: 1, cipher: aes-128-gcm, password: p}\n"

func TestFetchHeaders(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Subscription-Userinfo", "upload=1024; download=2048; total=1073741824; expire=1735689600")
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''%E6%88%91%E7%9A%84%E8%AE%A2%E9%98%85.yaml")
		w.Header().Set("Profile-Update-Interval", "12")
		w.Write([]byte(validSub))
	}))
	defer srv.Close()

	f, err := Fetch(context.Background(), srv.Client(), srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if gotUA != netx.UserAgent {
		t.Errorf("User-Agent = %q, want default %q", gotUA, netx.UserAgent)
	}
	if string(f.Data) != validSub {
		t.Errorf("data mismatch")
	}
	if f.Upload != 1024 || f.Download != 2048 || f.Total != 1073741824 {
		t.Errorf("userinfo = %d/%d/%d", f.Upload, f.Download, f.Total)
	}
	if !f.Expire.Equal(time.Unix(1735689600, 0)) {
		t.Errorf("expire = %v", f.Expire)
	}
	if f.Name != "我的订阅" {
		t.Errorf("name = %q", f.Name)
	}
	if f.UpdateInterval != 12 {
		t.Errorf("interval = %d", f.UpdateInterval)
	}

	// Custom User-Agent is sent verbatim.
	if _, err := Fetch(context.Background(), srv.Client(), srv.URL, "MyUA/1.0"); err != nil {
		t.Fatal(err)
	}
	if gotUA != "MyUA/1.0" {
		t.Errorf("custom UA not sent: %q", gotUA)
	}
}

func TestFetchNoOptionalHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(validSub))
	}))
	defer srv.Close()
	f, err := Fetch(context.Background(), srv.Client(), srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "" || f.Total != 0 || !f.Expire.IsZero() || f.UpdateInterval != 0 {
		t.Errorf("unexpected metadata: %+v", f)
	}
}

func TestParseDispositionName(t *testing.T) {
	cases := map[string]string{
		"":                                   "",
		`attachment; filename="sub.yaml"`:    "sub",
		`attachment; filename=sub.YML`:       "sub",
		`attachment; filename="My Sub.yaml"`: "My Sub",
		`attachment; filename*=UTF-8''%E8%AE%A2%E9%98%85.yaml`:   "订阅",
		`attachment; filename="x.yaml"; filename*=UTF-8''y.yaml`: "y",
		`attachment; filename=%E8%AE%A2%E9%98%85.yaml`:           "订阅",
		`attachment;filename=plain`:                              "plain",
		`inline`:                                                 "",
		`attachment; filename="../../etc/passwd.yaml"`:           "passwd",
		`attachment; filename="订阅.yaml"`:                         "订阅", // raw UTF-8, invalid per RFC but common
	}
	for h, want := range cases {
		if got := parseDispositionName(h); got != want {
			t.Errorf("%q: got %q, want %q", h, got, want)
		}
	}
}

func TestParseUserInfo(t *testing.T) {
	up, down, total, exp := parseUserInfo("upload=1; download=2;total=3; expire=1700000000.0; junk; x=y")
	if up != 1 || down != 2 || total != 3 || exp.Unix() != 1700000000 {
		t.Errorf("got %d %d %d %v", up, down, total, exp)
	}
	_, _, _, exp = parseUserInfo("upload=1; expire=0")
	if !exp.IsZero() {
		t.Errorf("expire=0 must be zero time")
	}
}

func TestFetchStatusErrors(t *testing.T) {
	for code, want := range map[int]string{
		403: "下载失败：HTTP 403。链接可能已失效",
		404: "下载失败：HTTP 404。链接不存在",
		429: "下载失败：HTTP 429。请求过于频繁",
		500: "下载失败：HTTP 500。服务商服务器出错",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			w.Write([]byte(validSub))
		}))
		_, err := Fetch(context.Background(), srv.Client(), srv.URL, "")
		srv.Close()
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("code %d: got %v, want prefix %q", code, err, want)
		}
	}
}

func TestFetchTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	_, err := Fetch(context.Background(), client, srv.URL, "")
	if err == nil || !strings.HasPrefix(err.Error(), "下载超时") {
		t.Fatalf("client timeout: got %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = Fetch(ctx, srv.Client(), srv.URL, "")
	if err == nil || !strings.HasPrefix(err.Error(), "下载超时") {
		t.Fatalf("context timeout: got %v", err)
	}
}

func TestFetchNotClashFormat(t *testing.T) {
	links := "ss://YWVzLTEyOC1nY206cGFzc3dvcmQ@1.1.1.1:8388#node1\nvmess://eyJ2IjoiMiJ9\n"
	cases := map[string]struct {
		body string
		want string
	}{
		"plain share links":    {links, "节点分享链接"},
		"base64 share links":   {base64.StdEncoding.EncodeToString([]byte(links)), "节点分享链接"},
		"base64 raw url":       {base64.RawURLEncoding.EncodeToString([]byte(links)), "节点分享链接"},
		"html page":            {"<html><body>login</body></html>", "不是 Clash/Mihomo 格式的订阅"},
		"yaml without proxies": {"port: 7890\nrules: []\n", "缺少 proxies 或 proxy-providers"},
		"empty":                {"", "订阅内容为空"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, err := Fetch(context.Background(), srv.Client(), srv.URL, "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestFetchTooLarge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("proxies:\n"))
		chunk := strings.Repeat("#", 1<<20)
		for i := 0; i < 33; i++ {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	_, err := Fetch(context.Background(), srv.Client(), srv.URL, "")
	if err == nil || !strings.Contains(err.Error(), "32 MB") {
		t.Fatalf("got %v", err)
	}
}

func TestFetchBadURL(t *testing.T) {
	for _, u := range []string{"", "ftp://x/y", "not a url", "example.com/sub"} {
		_, err := Fetch(context.Background(), netx.Direct(time.Second), u, "")
		if err == nil || !strings.HasPrefix(err.Error(), "订阅链接无效") {
			t.Errorf("%q: got %v", u, err)
		}
	}
}

func TestFetchErrorHidesToken(t *testing.T) {
	// Connection refused on a closed port: the message must not echo the URL/query.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL + "/api/v1/client/subscribe?token=SECRETTOKEN"
	srv.Close()
	_, err := Fetch(context.Background(), netx.Direct(2*time.Second), url, "")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Fatalf("token leaked: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "下载失败") {
		t.Fatalf("got %v", err)
	}
}

func TestCheckYAML(t *testing.T) {
	ok := []string{
		validSub,
		"proxy-providers:\n  p:\n    type: http\n    url: https://x\n",
		"proxies: []\nproxy-providers:\n  p: {type: http}\n",
	}
	for _, s := range ok {
		if err := CheckYAML([]byte(s)); err != nil {
			t.Errorf("%q: unexpected error %v", s, err)
		}
	}
	bad := map[string]string{
		"proxies: []\n":                      "缺少 proxies",
		"proxies: []\nproxy-providers: {}\n": "缺少 proxies",
		"- a\n":                              "键值映射",
		"a: [\n":                             "YAML 语法错误",
		"just text":                          "键值映射",
	}
	for s, want := range bad {
		err := CheckYAML([]byte(s))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", s, err, want)
		}
	}
}
