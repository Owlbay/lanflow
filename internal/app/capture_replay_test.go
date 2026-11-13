package app

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"lanflow/internal/model"
)

func TestReplayRequest(t *testing.T) {
	var got *http.Request
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		zw := gzip.NewWriter(w)
		zw.Write([]byte(`{"hello":"world"}`))
		zw.Close()
	}))
	defer srv.Close()

	h := newHarness(t, nil)
	s := h.s
	res, err := s.ReplayRequest(model.ReplayRequest{Method: "post", URL: srv.URL + "/api?x=1", Direct: true,
		Header: []model.KV{{Key: ":authority", Value: "x"}, {Key: "content-length", Value: "999"}, {Key: "Connection", Value: "keep-alive"},
			{Key: "authorization", Value: "Bearer t"}, {Key: "X-Edited", Value: "yes"}},
		Body: []byte(`{"a":1}`)})
	if err != nil || res.Error != "" {
		t.Fatalf("%v %s", err, res.Error)
	}
	if got.Method != "POST" || got.URL.RawQuery != "x=1" || gotBody != `{"a":1}` {
		t.Fatalf("sent %s %s %q", got.Method, got.URL, gotBody)
	}
	if got.Header.Get("Authorization") != "Bearer t" || got.Header.Get("X-Edited") != "yes" {
		t.Fatalf("headers %v", got.Header)
	}
	if ua, ok := got.Header["User-Agent"]; ok {
		t.Fatalf("User-Agent added: %q", ua)
	}
	if got.Header.Get("Accept-Encoding") != "" {
		t.Fatalf("Accept-Encoding added: %q", got.Header.Get("Accept-Encoding"))
	}
	if res.Status != 200 || string(res.Body) != `{"hello":"world"}` || res.Via != "direct" {
		t.Fatalf("result %+v body %q", res, res.Body)
	}

	// redirects are answers unless asked to follow
	res, _ = s.ReplayRequest(model.ReplayRequest{Method: "GET", URL: srv.URL + "/redirect", Direct: true})
	if res.Status != http.StatusFound {
		t.Fatalf("redirect followed: %d", res.Status)
	}
	res, _ = s.ReplayRequest(model.ReplayRequest{Method: "GET", URL: srv.URL + "/redirect", Direct: true, FollowRedirects: true})
	if res.Status != 200 {
		t.Fatalf("redirect not followed: %d", res.Status)
	}

	if _, err := s.ReplayRequest(model.ReplayRequest{URL: "ftp://x"}); err == nil {
		t.Fatal("ftp accepted")
	}
	// connection failure is a result, not an error
	res, err = s.ReplayRequest(model.ReplayRequest{URL: "http://127.0.0.1:1/", Direct: true, TimeoutMs: 2000})
	if err != nil || res.Error == "" {
		t.Fatalf("%v %+v", err, res)
	}
}
