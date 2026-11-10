package redact

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"strings"
	"testing"

	"lanflow/internal/model"
)

func TestHeadersAndPath(t *testing.T) {
	h := Headers([]model.KV{{Key: "Authorization", Value: "Bearer abcdefghijklmnop"}, {Key: "X-Session-Id", Value: "123456789"}, {Key: "Accept", Value: "*/*"}})
	if !strings.HasPrefix(h[0].Value, "Bear…(") || !strings.Contains(h[1].Value, "(9 B)") || h[2].Value != "*/*" {
		t.Fatal(h)
	}
	p := Path("/a?access_token=secretsecret&x=1")
	if strings.Contains(p, "secretsecret") || !strings.Contains(p, "x=1") {
		t.Fatal(p)
	}
}

func TestBodyAndStructure(t *testing.T) {
	b := Body([]byte(`{"user":"u","password":"hunter22hunter22","nested":{"refresh_token":"zzzzzzzzzz"}}`), "application/json")
	if bytes.Contains(b, []byte("hunter22")) || bytes.Contains(b, []byte("zzzzzzzzzz")) || !bytes.Contains(b, []byte(`"u"`)) {
		t.Fatal(string(b))
	}
	f := Body([]byte("a=1&client_secret=ssssssssss"), "application/x-www-form-urlencoded")
	if bytes.Contains(f, []byte("ssssssssss")) {
		t.Fatal(string(f))
	}
	st, _ := json.Marshal(Structure([]byte(`{"name":"bob","n":[1,2]}`), "application/json"))
	if strings.Contains(string(st), "bob") || !strings.Contains(string(st), `"name":"string(3)"`) {
		t.Fatal(string(st))
	}
}

func TestDecode(t *testing.T) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write([]byte("hello"))
	w.Close()
	if b, ok := Decode(buf.Bytes(), "gzip"); !ok || string(b) != "hello" {
		t.Fatal(b, ok)
	}
	if _, ok := Decode([]byte("x"), "br"); ok {
		t.Fatal("br decoded")
	}
}
