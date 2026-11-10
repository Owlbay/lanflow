package applog

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"更新订阅 https://sub.example.com/api/v1/client/subscribe?token=abc123 失败": "更新订阅 https://sub.example.com/*** 失败",
		"GET https://example.com/ ok":                       "GET https://example.com/ ok",
		"url=https://user:pw@host.example:8443/x":           "url=https://***@host.example:8443/***",
		"token=abc&x=1 secret: s3cr3t password=\"hunter2\"": "token=***&x=1 secret: *** password=\"***\"",
		"版本 v1.19.33 在 127.0.0.1:7890":                      "版本 v1.19.33 在 127.0.0.1:7890",
		"monkey=banana":                                     "monkey=banana",
		"url: enc:v1:q83vEjRWeJA+/w== 已加载":                  "url: enc:v1:*** 已加载",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestLoggerFileAndRing(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	l.OnWrite(func() { calls++ })
	l.Infof("启动内核 %s", "v1")
	l.Errorf("订阅 https://a.example/sub?token=x 失败\n第二行")
	if calls != 2 {
		t.Fatal("onWrite", calls)
	}
	es := l.Entries()
	if len(es) != 2 || es[1].Level != Error || strings.Contains(es[1].Payload, "token") || strings.Contains(es[1].Payload, "\n") {
		t.Fatalf("entries %+v", es)
	}
	data, _ := os.ReadFile(filepath.Join(dir, FileName))
	if lines := strings.Split(strings.TrimSpace(string(data)), "\n"); len(lines) != 2 || !strings.Contains(lines[0], "INFO") || !strings.Contains(lines[1], "ERROR") {
		t.Fatalf("file:\n%s", data)
	}
	l.Clear()
	if len(l.Entries()) != 0 {
		t.Fatal("clear")
	}

	// standard log package
	std := log.New(l.Writer(), "", log.LstdFlags)
	std.Printf("lanflow: tray: failed to load")
	if es := l.Entries(); len(es) != 1 || es[0].Payload != "tray: failed to load" || es[0].Level != Warn {
		t.Fatalf("std entry %+v", es)
	}
	l.Close()

	var nilL *Logger
	nilL.Infof("x")
	if nilL.Entries() != nil || nilL.Path() != "" {
		t.Fatal("nil logger")
	}
}

func TestRotate(t *testing.T) {
	dir := t.TempDir()
	l, _ := Open(dir)
	defer l.Close()
	big := strings.Repeat("x", 1<<20)
	for i := 0; i < 6; i++ {
		l.Infof("%s", big)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName+".1")); err != nil {
		t.Fatal("not rotated")
	}
	st, _ := os.Stat(filepath.Join(dir, FileName))
	if st.Size() > maxSize {
		t.Fatal("too big", st.Size())
	}
	if len(l.Entries()) != 6 {
		t.Fatal(len(l.Entries()))
	}
}
