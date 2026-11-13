package app

import (
	"testing"

	"lanflow/internal/model"
)

func TestParseCaptureTarget(t *testing.T) {
	for _, c := range []struct {
		in     string
		host   string
		port   int
		scheme string
	}{
		{"https://Chatgpt.com/c/1?x", "chatgpt.com", 443, "https"},
		{"http://rd.test.za.biz/", "rd.test.za.biz", 80, "http"},
		{"http://rd.test.za.biz:8080/a", "rd.test.za.biz", 8080, "http"},
		{"rd.test.za.biz:80", "rd.test.za.biz", 80, "http"},
		{"example.com.", "example.com", 443, "https"},
		{"example.com/path", "example.com", 443, "https"},
	} {
		h, p, s, err := parseCaptureTarget(c.in)
		if err != nil || h != c.host || p != c.port || s != c.scheme {
			t.Errorf("%q: %s %d %s %v", c.in, h, p, s, err)
		}
	}
	if _, _, _, err := parseCaptureTarget(" "); err == nil {
		t.Error("empty target accepted")
	}
}

func TestPortIn(t *testing.T) {
	if !portIn("80/443/8000-9000", 8443) || portIn("80/443", 8443) || !portIn(" 80 / 443", 443) {
		t.Fatal("portIn")
	}
}

func TestNormCapturePatterns(t *testing.T) {
	got := normCapturePatterns([]string{"https://API.example.com/v1", "api.example.com.", "+.Example.org", ""})
	if len(got) != 2 || got[0] != "api.example.com" || got[1] != "+.example.org" {
		t.Fatalf("%v", got)
	}
	if parentSuggest("api.example.com", []string{"example.com"}) != "example.com" || parentSuggest("api.example.com", []string{"+.example.com"}) != "" {
		t.Fatal("parentSuggest")
	}
}

func TestCaptureBypassSplit(t *testing.T) {
	set := model.CaptureSettings{Bypass: []string{"mine.com"}, DisabledBuiltin: []string{"192.168.0.0/16"},
		Domains: []string{"a.za.biz", "b.com"}, AllowPrivate: []string{"A.za.biz", "c.com"}}
	always, private := CaptureBypassSplit(set)
	for _, p := range private {
		if !model.IsCapturePrivate(p) || p == "192.168.0.0/16" {
			t.Fatalf("private: %v", private)
		}
	}
	for _, a := range always {
		if model.IsCapturePrivate(a) {
			t.Fatalf("always holds %s", a)
		}
	}
	if always[len(always)-1] != "mine.com" {
		t.Fatalf("user bypass missing: %v", always)
	}
	if lan := captureAllowPrivate(set); len(lan) != 1 || lan[0] != "a.za.biz" {
		t.Fatalf("allow: %v", lan)
	}
}
