package ca

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"sync"
)

// SecretStore is a store of small opaque secrets (the login Keychain via
// MyGo Secrets on macOS).
type SecretStore interface {
	Get(key string) ([]byte, error) // ErrNoKey (or an error wrapping it) when absent
	Set(key string, value []byte) error
	Delete(key string) error
}

// SecretKeys keeps the key as PKCS#8 DER in a SecretStore. The key is
// exportable by whoever can read the store: on macOS the Keychain asks
// before another program reads it (docs/capture.md §6).
type SecretKeys struct{ S SecretStore }

func (k SecretKeys) Create(name string) (crypto.Signer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := k.S.Set(name, der); err != nil {
		return nil, err
	}
	return key, nil
}

func (k SecretKeys) Open(name string) (crypto.Signer, error) {
	der, err := k.S.Get(name)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	s, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("capture ca: stored key is not a signer")
	}
	return s, nil
}

func (k SecretKeys) Delete(name string) error { return k.S.Delete(name) }

// MemKeys is an in-memory KeyStore for tests.
type MemKeys struct {
	mu sync.Mutex
	m  map[string]crypto.Signer
}

func (k *MemKeys) Create(name string) (crypto.Signer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m == nil {
		k.m = map[string]crypto.Signer{}
	}
	k.m[name] = key
	return key, nil
}

func (k *MemKeys) Open(name string) (crypto.Signer, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if s, ok := k.m[name]; ok {
		return s, nil
	}
	return nil, ErrNoKey
}

func (k *MemKeys) Delete(name string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, name)
	return nil
}

// Len is the number of keys held.
func (k *MemKeys) Len() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.m)
}
