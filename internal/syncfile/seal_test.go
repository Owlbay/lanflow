package syncfile

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"lanflow/internal/model"
)

const testPass = "correct horse battery"

func TestSealRoundTrip(t *testing.T) {
	enc, key, err := NewEncryption(testPass)
	if err != nil {
		t.Fatal(err)
	}
	if enc.KDF != KDFArgon2id || enc.Time != defaultTime || enc.MemoryKiB != defaultMemoryKiB || enc.Threads != defaultThreads {
		t.Fatalf("parameters: %+v", enc)
	}
	if err := enc.Validate(); err != nil {
		t.Fatal(err)
	}
	link := "https://sub.example.com/api?token=s3cret"
	v, err := Seal(key, SubscriptionPath("a1"), link)
	if err != nil {
		t.Fatal(err)
	}
	if !IsSealed(v) || strings.Contains(v, "s3cret") {
		t.Fatalf("sealed value: %s", v)
	}
	if v2, _ := Seal(key, SubscriptionPath("a1"), link); v2 == v {
		t.Fatal("nonce reused")
	}
	got, err := Open(key, SubscriptionPath("a1"), v)
	if err != nil || got != link {
		t.Fatalf("Open = %q, %v", got, err)
	}

	// the passphrase gives the same key again; through the file too
	again, err := enc.Unlock(testPass)
	if err != nil || !bytes.Equal(again, key) {
		t.Fatalf("Unlock: %v", err)
	}
	doc := sampleDoc()
	doc.Encryption = &enc
	doc.Subscriptions[0].URL = v
	doc.WebDAVPassword, _ = Seal(key, WebDAVPasswordPath, "davpw")
	data := Encode(doc)
	back, err := Decode(data, model.Settings{})
	if err != nil {
		t.Fatalf("Decode: %v\n%s", err, data)
	}
	if back.Encryption == nil || *back.Encryption != enc || !bytes.Equal(Encode(back), data) {
		t.Fatalf("encryption block not kept:\n%s", data)
	}
	if !strings.HasPrefix(string(data[strings.Index(string(data), "format:"):]), "format: 1\nencryption:") {
		t.Fatalf("encryption block should follow format:\n%s", data)
	}
	vals := SealedValues(back)
	if len(vals) != 2 {
		t.Fatalf("SealedValues = %v", vals)
	}
	if pw, err := Open(key, WebDAVPasswordPath, vals[WebDAVPasswordPath]); err != nil || pw != "davpw" {
		t.Fatalf("webdav: %q %v", pw, err)
	}
}

func TestSealAADBindsField(t *testing.T) {
	_, key, err := NewEncryption(testPass)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := Seal(key, SubscriptionPath("a1"), "https://a.example/")
	for _, path := range []string{SubscriptionPath("b2"), WebDAVPasswordPath, checkPath} {
		if _, err := Open(key, path, v); !errors.Is(err, ErrSealedDamaged) {
			t.Fatalf("value of a1 opened as %s: %v", path, err)
		}
	}
	// a flipped byte is caught too
	raw := []byte(v)
	raw[len(raw)-5] ^= 1
	if raw[len(raw)-5] == '=' {
		raw[len(raw)-6] ^= 1
	}
	if _, err := Open(key, SubscriptionPath("a1"), string(raw)); err == nil {
		t.Fatal("tampered value opened")
	}
}

func TestWrongPassphrase(t *testing.T) {
	enc, _, err := NewEncryption(testPass)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Unlock("correct horse battery!"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	other, otherKey, _ := NewEncryption(testPass) // same passphrase, new salt
	if enc.Verify(otherKey) || other.Salt == enc.Salt {
		t.Fatal("a key of another salt verified")
	}
	if _, _, err := NewEncryption("short"); !errors.Is(err, ErrShortPassphrase) {
		t.Fatalf("short passphrase: %v", err)
	}
}

func TestEncryptionLimits(t *testing.T) {
	enc, _, _ := NewEncryption(testPass)
	for name, mut := range map[string]func(*Encryption){
		"kdf":    func(e *Encryption) { e.KDF = "scrypt" },
		"memory": func(e *Encryption) { e.MemoryKiB = 4 << 20 },
		"time":   func(e *Encryption) { e.Time = 1000 },
		"salt":   func(e *Encryption) { e.Salt = "c2hvcnQ=" },
		"check":  func(e *Encryption) { e.Check = "nope" },
	} {
		e := enc
		mut(&e)
		if e.Validate() == nil {
			t.Errorf("%s: accepted %+v", name, e)
		}
		if name != "check" {
			if _, err := e.DeriveKey(testPass); err == nil {
				t.Errorf("%s: derived a key", name)
			}
		}
	}
}

func TestSealedDecodeErrors(t *testing.T) {
	enc, key, _ := NewEncryption(testPass)
	v, _ := Seal(key, SubscriptionPath("a1b2c3d4"), "https://x.example/")
	for name, tc := range map[string]struct {
		mut  func(*Doc)
		want string
	}{
		"no header":  {func(d *Doc) { d.Subscriptions[0].URL = v }, "subscriptions[0].url"},
		"bad base64": {func(d *Doc) { d.Encryption = &enc; d.Subscriptions[0].URL = "enc:v1:!!" }, "subscriptions[0].url"},
		"bad params": {func(d *Doc) { e := enc; e.MemoryKiB = 1; d.Encryption = &e }, "encryption"},
		"webdav":     {func(d *Doc) { d.WebDAVPassword = "hunter2" }, "webdavPassword"},
	} {
		d := sampleDoc()
		tc.mut(&d)
		_, err := Decode(Encode(d), model.Settings{})
		var se *Error
		if !errors.As(err, &se) || se.Path != tc.want || se.Line == 0 {
			t.Errorf("%s: %v", name, err)
		}
	}
}
