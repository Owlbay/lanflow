// Package ca is the local root of HTTPS 选择性解密 (docs/capture.md §6):
// an ECDSA P-256 root valid 90 days with name constraints that exclude
// every IP address and a list of high-value domains, and the leaves it
// signs on demand for the hosts being decrypted.
//
// The private key lives in a KeyStore: the login Keychain on macOS (as an
// opaque secret), a non-exportable CNG key on Windows; tests use
// MemKeys. Only the capture agent process loads it.
package ca

import (
	"container/list"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"
)

// Validity of the root and of the leaves.
const (
	RootValidity = 90 * 24 * time.Hour
	LeafValidity = 7 * 24 * time.Hour
	leafBackdate = time.Hour
	cacheSize    = 1024
	Organization = "Lanflow"
)

// ExcludedDomains are names the root may never sign for (excludedSubtrees):
// identity providers, banks and payment. A name written with a leading
// dot excludes subdomains only; without it, the name and its subdomains
// (RFC 5280 dNSName constraint semantics as Go and SecTrust apply them).
var ExcludedDomains = []string{
	"apple.com", "icloud.com", "appleid.apple.com",
	"login.microsoftonline.com", "login.live.com", "accounts.google.com",
	"okta.com", "1password.com", "bitwarden.com", "lastpass.com",
	"paypal.com", "stripe.com", "alipay.com", "95516.com", "unionpay.com",
	"icbc.com.cn", "ccb.com", "abchina.com", "boc.cn", "bankcomm.com", "cmbchina.com",
	"chase.com", "bankofamerica.com", "wellsfargo.com", "citi.com", "hsbc.com",
}

// ErrNoKey is returned by a KeyStore that holds no key under the name.
var ErrNoKey = errors.New("capture ca: key not found")

// ErrKeyMismatch: the stored key is not the certificate's.
var ErrKeyMismatch = errors.New("capture ca: stored key does not match the certificate")

// ErrBadCert: the certificate handed to Load does not parse.
var ErrBadCert = errors.New("capture ca: bad root certificate")

// KeyStore keeps the root's private key.
type KeyStore interface {
	// Create makes a new P-256 key under name and returns a signer for
	// it; a key already there is replaced.
	Create(name string) (crypto.Signer, error)
	// Open returns the signer of the key stored under name (ErrNoKey).
	Open(name string) (crypto.Signer, error)
	// Delete removes the key; a missing key is not an error.
	Delete(name string) error
}

// Root is a loaded root: the certificate and a signer for its key.
type Root struct {
	Cert   *x509.Certificate
	signer crypto.Signer

	mu    sync.Mutex
	lru   *list.List // of *leafEntry, most recent first
	index map[string]*list.Element
	slots map[string]*sync.WaitGroup // singleflight per host
	now   func() time.Time
	leafK *ecdsa.PrivateKey // one leaf key per session (docs/capture.md §6)
}

type leafEntry struct {
	host string
	cert *tls.Certificate
	exp  time.Time
}

// KeyName is the KeyStore name of the root's key.
func KeyName(cert *x509.Certificate) string {
	return "lanflow-capture-ca-" + cert.SerialNumber.Text(16)
}

// Generate creates a new root for host (shown in the name) with its key in
// ks, and returns it with the certificate in PEM.
func Generate(ks KeyStore, host string, now time.Time) (*Root, []byte, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	serial.SetBit(serial, 127, 1) // a fixed length keeps KeyName stable
	name := "lanflow-capture-ca-" + serial.Text(16)
	signer, err := ks.Create(name)
	if err != nil {
		return nil, nil, fmt.Errorf("create CA key: %w", err)
	}
	if _, ok := signer.Public().(*ecdsa.PublicKey); !ok {
		return nil, nil, errors.New("create CA key: not an ECDSA key")
	}
	_, v4, _ := net.ParseCIDR("0.0.0.0/0")
	_, v6, _ := net.ParseCIDR("::/0")
	if host == "" {
		host = "localhost"
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   fmt.Sprintf("Lanflow Local CA (%s %s)", host, now.Format("2006-01-02")),
			Organization: []string{Organization},
		},
		NotBefore:             now.Add(-leafBackdate),
		NotAfter:              now.Add(RootValidity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		ExcludedDNSDomains:    ExcludedDomains,
		ExcludedIPRanges:      []*net.IPNet{v4, v6},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, signer.Public(), signer)
	if err != nil {
		ks.Delete(name)
		return nil, nil, fmt.Errorf("create CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		ks.Delete(name)
		return nil, nil, err
	}
	r, err := newRoot(cert, signer)
	if err != nil {
		return nil, nil, err
	}
	return r, EncodePEM(der), nil
}

// Load opens the root whose certificate is certPEM, with its key from ks.
func Load(ks KeyStore, certPEM []byte) (*Root, error) {
	cert, err := ParsePEM(certPEM)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadCert, err)
	}
	signer, err := ks.Open(KeyName(cert))
	if err != nil {
		return nil, err
	}
	if !samePublic(signer.Public(), cert.PublicKey) {
		return nil, ErrKeyMismatch
	}
	return newRoot(cert, signer)
}

func samePublic(a, b crypto.PublicKey) bool {
	ea, ok1 := a.(*ecdsa.PublicKey)
	eb, ok2 := b.(*ecdsa.PublicKey)
	return ok1 && ok2 && ea.Equal(eb)
}

func newRoot(cert *x509.Certificate, signer crypto.Signer) (*Root, error) {
	lk, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Root{Cert: cert, signer: signer, lru: list.New(), index: map[string]*list.Element{},
		slots: map[string]*sync.WaitGroup{}, now: time.Now, leafK: lk}, nil
}

// EncodePEM wraps a DER certificate in PEM.
func EncodePEM(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// ParsePEM reads the first certificate of a PEM text.
func ParsePEM(b []byte) (*x509.Certificate, error) {
	for {
		var blk *pem.Block
		blk, b = pem.Decode(b)
		if blk == nil {
			return nil, errors.New("capture ca: no certificate in PEM")
		}
		if blk.Type == "CERTIFICATE" {
			return x509.ParseCertificate(blk.Bytes)
		}
	}
}

// Fingerprint is the SHA-256 of a certificate, hex.
func Fingerprint(der []byte) string {
	s := sha256.Sum256(der)
	return hex.EncodeToString(s[:])
}

// IsOurs reports whether cert was issued by (or is) a Lanflow root: the
// upstream check refuses chains ending there (docs/capture.md §5.4).
func IsOurs(cert *x509.Certificate) bool {
	for _, o := range cert.Subject.Organization {
		if o == Organization && strings.HasPrefix(cert.Subject.CommonName, "Lanflow Local CA") {
			return true
		}
	}
	for _, o := range cert.Issuer.Organization {
		if o == Organization && strings.HasPrefix(cert.Issuer.CommonName, "Lanflow Local CA") {
			return true
		}
	}
	return false
}

// Leaf returns a certificate for host, from the cache or freshly signed.
// IP addresses are refused (the root excludes them); names are signed
// exactly, never as wildcards.
func (r *Root) Leaf(host string) (*tls.Certificate, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return nil, errors.New("capture ca: no host name")
	}
	if net.ParseIP(host) != nil {
		return nil, errors.New("capture ca: IP addresses are not signed")
	}
	for {
		r.mu.Lock()
		if el, ok := r.index[host]; ok {
			e := el.Value.(*leafEntry)
			if r.now().Before(e.exp.Add(-time.Hour)) {
				r.lru.MoveToFront(el)
				r.mu.Unlock()
				return e.cert, nil
			}
			r.lru.Remove(el)
			delete(r.index, host)
		}
		if wg, ok := r.slots[host]; ok {
			r.mu.Unlock()
			wg.Wait()
			continue
		}
		wg := &sync.WaitGroup{}
		wg.Add(1)
		r.slots[host] = wg
		r.mu.Unlock()

		cert, exp, err := r.sign(host)
		r.mu.Lock()
		delete(r.slots, host)
		if err == nil {
			r.index[host] = r.lru.PushFront(&leafEntry{host: host, cert: cert, exp: exp})
			for r.lru.Len() > cacheSize {
				old := r.lru.Remove(r.lru.Back()).(*leafEntry)
				delete(r.index, old.host)
			}
		}
		r.mu.Unlock()
		wg.Done()
		return cert, err
	}
}

func (r *Root) sign(host string) (*tls.Certificate, time.Time, error) {
	now := r.now()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, time.Time{}, err
	}
	exp := now.Add(LeafValidity)
	if exp.After(r.Cert.NotAfter) {
		exp = r.Cert.NotAfter
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    now.Add(-leafBackdate),
		NotAfter:     exp,
		DNSNames:     []string{host},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, r.Cert, &r.leafK.PublicKey, r.signer)
	if err != nil {
		return nil, time.Time{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, time.Time{}, err
	}
	return &tls.Certificate{Certificate: [][]byte{der, r.Cert.Raw}, PrivateKey: r.leafK, Leaf: leaf}, exp, nil
}

// Flush empties the leaf cache (after a regeneration).
func (r *Root) Flush() {
	r.mu.Lock()
	r.lru.Init()
	r.index = map[string]*list.Element{}
	r.mu.Unlock()
}

// TrustCheck signs a leaf for trust-check.lanflow.invalid and verifies it
// against roots (nil = the system's): the trust state comes from what the
// system accepts, never from an install command's exit code
// (docs/capture.md §6). The certificate alone is enough: the caller (the
// window) never loads the key, so this takes a pre-signed leaf.
func TrustCheck(leaf *x509.Certificate, roots *x509.CertPool) error {
	_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: TrustCheckHost,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

// TrustCheckHost is the name of the leaf used to test trust.
const TrustCheckHost = "trust-check.lanflow.invalid"

// TrustLeaf signs the long-lived (as long as the root) trust-check leaf.
func (r *Root) TrustLeaf() ([]byte, error) {
	cert, err := r.Leaf(TrustCheckHost)
	if err != nil {
		return nil, err
	}
	return cert.Certificate[0], nil
}
