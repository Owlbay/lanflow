package ca

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The root's key on Windows is a persisted CNG key in the Microsoft
// Software Key Storage Provider with an export policy of 0: it cannot be
// exported, only used for signing by processes of this user
// (docs/capture.md §6). Untested on a real Windows machine (§15).

func systemKeys(SecretStore) KeyStore { return cngKeys{} }

var (
	ncrypt                  = windows.NewLazySystemDLL("ncrypt.dll")
	procOpenStorageProvider = ncrypt.NewProc("NCryptOpenStorageProvider")
	procCreatePersistedKey  = ncrypt.NewProc("NCryptCreatePersistedKey")
	procOpenKey             = ncrypt.NewProc("NCryptOpenKey")
	procSetProperty         = ncrypt.NewProc("NCryptSetProperty")
	procFinalizeKey         = ncrypt.NewProc("NCryptFinalizeKey")
	procExportKey           = ncrypt.NewProc("NCryptExportKey")
	procSignHash            = ncrypt.NewProc("NCryptSignHash")
	procDeleteKey           = ncrypt.NewProc("NCryptDeleteKey")
	procFreeObject          = ncrypt.NewProc("NCryptFreeObject")
)

const (
	ncryptOverwriteKeyFlag = 0x80
	ncryptPersistFlag      = 0x80000000
	nteBadKeyset           = 0x80090016
	eccPublicP256Magic     = 0x31534345 // BCRYPT_ECDSA_PUBLIC_P256_MAGIC
)

func u16(s string) *uint16 { p, _ := windows.UTF16PtrFromString(s); return p }

func status(r uintptr) error {
	if r == 0 {
		return nil
	}
	if uint32(r) == nteBadKeyset {
		return ErrNoKey
	}
	return fmt.Errorf("ncrypt: 0x%08x", uint32(r))
}

type cngKeys struct{}

func provider() (uintptr, error) {
	var p uintptr
	r, _, _ := procOpenStorageProvider.Call(uintptr(unsafe.Pointer(&p)), uintptr(unsafe.Pointer(u16("Microsoft Software Key Storage Provider"))), 0)
	return p, status(r)
}

func (cngKeys) Create(name string) (crypto.Signer, error) {
	prov, err := provider()
	if err != nil {
		return nil, err
	}
	defer procFreeObject.Call(prov)
	var key uintptr
	r, _, _ := procCreatePersistedKey.Call(prov, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(u16("ECDSA_P256"))),
		uintptr(unsafe.Pointer(u16(name))), 0, ncryptOverwriteKeyFlag)
	if err := status(r); err != nil {
		return nil, err
	}
	policy := uint32(0) // not exportable
	r, _, _ = procSetProperty.Call(key, uintptr(unsafe.Pointer(u16("Export Policy"))), uintptr(unsafe.Pointer(&policy)), 4, ncryptPersistFlag)
	if err := status(r); err != nil {
		procFreeObject.Call(key)
		return nil, err
	}
	r, _, _ = procFinalizeKey.Call(key, 0)
	if err := status(r); err != nil {
		procFreeObject.Call(key)
		return nil, err
	}
	return newCNGSigner(key)
}

func (cngKeys) Open(name string) (crypto.Signer, error) {
	prov, err := provider()
	if err != nil {
		return nil, err
	}
	defer procFreeObject.Call(prov)
	var key uintptr
	r, _, _ := procOpenKey.Call(prov, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(u16(name))), 0, 0)
	if err := status(r); err != nil {
		return nil, err
	}
	return newCNGSigner(key)
}

func (cngKeys) Delete(name string) error {
	prov, err := provider()
	if err != nil {
		return err
	}
	defer procFreeObject.Call(prov)
	var key uintptr
	r, _, _ := procOpenKey.Call(prov, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(u16(name))), 0, 0)
	if err := status(r); err != nil {
		if errors.Is(err, ErrNoKey) {
			return nil
		}
		return err
	}
	r, _, _ = procDeleteKey.Call(key, 0) // frees the handle
	return status(r)
}

type cngSigner struct {
	key uintptr
	pub *ecdsa.PublicKey
}

func newCNGSigner(key uintptr) (*cngSigner, error) {
	var n uint32
	blobType := uintptr(unsafe.Pointer(u16("ECCPUBLICBLOB")))
	r, _, _ := procExportKey.Call(key, 0, blobType, 0, 0, 0, uintptr(unsafe.Pointer(&n)), 0)
	if err := status(r); err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	r, _, _ = procExportKey.Call(key, 0, blobType, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(n), uintptr(unsafe.Pointer(&n)), 0)
	if err := status(r); err != nil {
		return nil, err
	}
	if len(buf) < 8 || binary.LittleEndian.Uint32(buf) != eccPublicP256Magic {
		return nil, errors.New("ncrypt: unexpected public key blob")
	}
	sz := int(binary.LittleEndian.Uint32(buf[4:]))
	if len(buf) < 8+2*sz {
		return nil, errors.New("ncrypt: short public key blob")
	}
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(buf[8 : 8+sz]), Y: new(big.Int).SetBytes(buf[8+sz : 8+2*sz])}
	return &cngSigner{key: key, pub: pub}, nil
}

func (s *cngSigner) Public() crypto.PublicKey { return s.pub }

// Sign signs a digest; CNG returns r||s, Go wants ASN.1.
func (s *cngSigner) Sign(_ io.Reader, digest []byte, _ crypto.SignerOpts) ([]byte, error) {
	var n uint32
	r, _, _ := procSignHash.Call(s.key, 0, uintptr(unsafe.Pointer(&digest[0])), uintptr(len(digest)), 0, 0, uintptr(unsafe.Pointer(&n)), 0)
	if err := status(r); err != nil {
		return nil, err
	}
	sig := make([]byte, n)
	r, _, _ = procSignHash.Call(s.key, 0, uintptr(unsafe.Pointer(&digest[0])), uintptr(len(digest)), uintptr(unsafe.Pointer(&sig[0])), uintptr(n), uintptr(unsafe.Pointer(&n)), 0)
	if err := status(r); err != nil {
		return nil, err
	}
	sig = sig[:n]
	half := len(sig) / 2
	return asn1.Marshal(struct{ R, S *big.Int }{new(big.Int).SetBytes(sig[:half]), new(big.Int).SetBytes(sig[half:])})
}
