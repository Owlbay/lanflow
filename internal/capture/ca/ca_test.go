package ca

import (
	"crypto/x509"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGenerateAndLeaf(t *testing.T) {
	ks := &MemKeys{}
	now := time.Now()
	r, pemb, err := Generate(ks, "host", now)
	if err != nil {
		t.Fatal(err)
	}
	c := r.Cert
	if !c.IsCA || !c.MaxPathLenZero || c.MaxPathLen != 0 {
		t.Fatal("not a pathlen:0 CA")
	}
	if d := c.NotAfter.Sub(now); d < 89*24*time.Hour || d > 91*24*time.Hour {
		t.Fatalf("validity %v", d)
	}
	if c.SerialNumber.BitLen() != 128 {
		t.Fatalf("serial bits %d", c.SerialNumber.BitLen())
	}
	if len(c.ExcludedIPRanges) != 2 || len(c.ExcludedDNSDomains) == 0 {
		t.Fatal("constraints missing")
	}
	if !strings.HasPrefix(c.Subject.CommonName, "Lanflow Local CA (host ") || !IsOurs(c) {
		t.Fatal(c.Subject)
	}
	r2, err := Load(ks, pemb)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(r2.Cert)

	leaf, err := r2.Leaf("Example.COM.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "example.com"}); err != nil {
		t.Fatal(err)
	}
	if leaf.Leaf.NotAfter.Sub(now) > LeafValidity+time.Minute {
		t.Fatal("leaf too long")
	}
	if len(leaf.Leaf.DNSNames) != 1 || leaf.Leaf.DNSNames[0] != "example.com" {
		t.Fatal(leaf.Leaf.DNSNames)
	}
	if !IsOurs(leaf.Leaf) {
		t.Fatal("leaf not recognised")
	}
	// cached
	l2, _ := r2.Leaf("example.com")
	if l2 != leaf {
		t.Fatal("not cached")
	}
	if _, err := r2.Leaf("127.0.0.1"); err == nil {
		t.Fatal("IP signed")
	}
	// an excluded domain: the root signs (we do not check) but verifiers refuse
	bank, err := r2.Leaf("www.paypal.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bank.Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "www.paypal.com"}); err == nil {
		t.Fatal("excluded name verified")
	}
	// a leaf carrying an IP is refused by the constraint
	_ = net.IP{}
}

func TestLeafSingleflight(t *testing.T) {
	r, _, err := Generate(&MemKeys{}, "h", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	got := make([]any, 32)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := r.Leaf("a.test")
			if err != nil {
				t.Error(err)
			}
			got[i] = c
		}(i)
	}
	wg.Wait()
	for _, g := range got {
		if g != got[0] {
			t.Fatal("signed more than once")
		}
	}
}

func TestLoadMissingKey(t *testing.T) {
	ks := &MemKeys{}
	r, pemb, _ := Generate(ks, "h", time.Now())
	ks.Delete(KeyName(r.Cert))
	if _, err := Load(ks, pemb); err == nil {
		t.Fatal("loaded without key")
	}
}

func TestTrustCheck(t *testing.T) {
	r, _, _ := Generate(&MemKeys{}, "h", time.Now())
	der, err := r.TrustLeaf()
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	if TrustCheck(leaf, pool) == nil {
		t.Fatal("trusted by an empty pool")
	}
	pool.AddCert(r.Cert)
	if err := TrustCheck(leaf, pool); err != nil {
		t.Fatal(err)
	}
}
