package syncfile

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"

	"lanflow/internal/i18n"
)

// Secrets sealed with the sync passphrase (docs/config-sync.md 用口令加密
// 链接). Instead of a keychain:<id> reference, a secret field holds
//
//	enc:v1:<base64(nonce ‖ AES-256-GCM ciphertext)>
//
// under a key derived from the user's passphrase with Argon2id. The
// file's encryption block carries the KDF parameters, the salt and a
// check value (a known text sealed with the key), so a device can tell a
// wrong passphrase — or one changed on another device — from a damaged
// file. The additional data of each value is its field path
// (subscriptions.<id>.url, webdavPassword): a ciphertext moved to another
// field or another subscription fails to open.
//
// Argon2id (RFC 9106) rather than PBKDF2: it is memory-hard, so guessing
// passphrases of a file found in a cloud drive costs 64 MiB per guess,
// not just CPU time. golang.org/x/crypto was already in the module graph
// (through golang.org/x/net).

const (
	// SealedPrefix starts a secret sealed with the sync passphrase.
	SealedPrefix = "enc:v1:"
	// KDFArgon2id is the only key derivation this version writes and reads.
	KDFArgon2id = "argon2id"
	// WebDAVPasswordPath is the sealed WebDAV password's field path.
	WebDAVPasswordPath = "webdavPassword"

	// RFC 9106 §4, second recommended option (64 MiB, t=3, p=4).
	defaultTime      = 3
	defaultMemoryKiB = 64 * 1024
	defaultThreads   = 4
	saltLen          = 16
	keyLen           = 32
	nonceLen         = 12

	// limits on parameters read from a file: a file anyone can write must
	// not make this machine allocate gigabytes or spin for minutes
	maxTime      = 16
	minMemoryKiB = 8 * 1024
	maxMemoryKiB = 1024 * 1024
	maxThreads   = 16

	// MinPassphraseLen is the shortest passphrase the app accepts (runes;
	// ErrShortPassphrase says the number).
	MinPassphraseLen = 8

	aadPrefix  = "lanflow.yaml/v1/"
	checkPath  = "encryption.check"
	checkPlain = "lanflow sync passphrase check"
)

// Encryption is the file's encryption block: present when the secrets
// are sealed with the sync passphrase.
type Encryption struct {
	KDF       string `sync:"kdf"`
	Time      int    `sync:"time"`
	MemoryKiB int    `sync:"memoryKiB"`
	Threads   int    `sync:"threads"`
	Salt      string `sync:"salt"`  // base64, 16 bytes
	Check     string `sync:"check"` // enc:v1: of a known text, to recognise the right passphrase
}

// Errors of sealing and opening.
var (
	ErrWrongPassphrase = i18n.Error("同步口令不正确。")
	ErrSealedDamaged   = i18n.Error("加密的值已损坏，或不属于这个字段。")
	ErrShortPassphrase = i18n.Error("同步口令至少需要 8 个字符。")
)

// SubscriptionPath is the field path of subscription id's link, the
// additional data of its sealed value. It names the subscription by ID,
// not by position, so reordering the list keeps the values valid.
func SubscriptionPath(id string) string { return "subscriptions." + id + ".url" }

// IsSealed reports whether v is a sealed value.
func IsSealed(v string) bool { return strings.HasPrefix(v, SealedPrefix) }

// NewEncryption starts an encryption block for passphrase: a fresh salt,
// the default parameters, and the check value. It returns the derived
// key with it.
func NewEncryption(passphrase string) (Encryption, []byte, error) {
	if utf8.RuneCountInString(passphrase) < MinPassphraseLen {
		return Encryption{}, nil, ErrShortPassphrase
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return Encryption{}, nil, err
	}
	e := Encryption{KDF: KDFArgon2id, Time: defaultTime, MemoryKiB: defaultMemoryKiB, Threads: defaultThreads,
		Salt: base64.StdEncoding.EncodeToString(salt)}
	key, err := e.DeriveKey(passphrase)
	if err != nil {
		return Encryption{}, nil, err
	}
	e.Check, err = Seal(key, checkPath, checkPlain)
	if err != nil {
		return Encryption{}, nil, err
	}
	return e, key, nil
}

// Validate checks the block's parameters (the limits keep a hostile file
// from exhausting this machine).
func (e Encryption) Validate() error {
	if e.KDF != KDFArgon2id {
		return errors.New(i18n.Tf("不支持的密钥派生方式「%s」", e.KDF))
	}
	if e.Time < 1 || e.Time > maxTime || e.MemoryKiB < minMemoryKiB || e.MemoryKiB > maxMemoryKiB || e.Threads < 1 || e.Threads > maxThreads {
		return errors.New(i18n.T("密钥派生参数超出允许范围"))
	}
	if salt, err := base64.StdEncoding.DecodeString(e.Salt); err != nil || len(salt) < saltLen {
		return errors.New(i18n.T("salt 应为至少 16 字节的 base64"))
	}
	if err := checkSealedFormat(e.Check); err != nil {
		return err
	}
	return nil
}

// DeriveKey derives the AES-256 key from passphrase with the block's
// parameters (slow on purpose: ~0.1 s and 64 MiB).
func (e Encryption) DeriveKey(passphrase string) ([]byte, error) {
	if e.KDF != KDFArgon2id || e.Time < 1 || e.Time > maxTime || e.MemoryKiB < minMemoryKiB || e.MemoryKiB > maxMemoryKiB || e.Threads < 1 || e.Threads > maxThreads {
		return nil, errors.New(i18n.T("密钥派生参数超出允许范围"))
	}
	salt, err := base64.StdEncoding.DecodeString(e.Salt)
	if err != nil || len(salt) < saltLen {
		return nil, errors.New(i18n.T("salt 应为至少 16 字节的 base64"))
	}
	return argon2.IDKey([]byte(passphrase), salt, uint32(e.Time), uint32(e.MemoryKiB), uint8(e.Threads), keyLen), nil
}

// Verify reports whether key opens the block's check value: the key of
// this passphrase and salt.
func (e Encryption) Verify(key []byte) bool {
	v, err := Open(key, checkPath, e.Check)
	return err == nil && subtle.ConstantTimeCompare([]byte(v), []byte(checkPlain)) == 1
}

// Unlock derives the key from passphrase and verifies it.
func (e Encryption) Unlock(passphrase string) ([]byte, error) {
	key, err := e.DeriveKey(passphrase)
	if err != nil {
		return nil, err
	}
	if !e.Verify(key) {
		return nil, ErrWrongPassphrase
	}
	return key, nil
}

// Seal encrypts plain for the field at path.
func Seal(key []byte, path, plain string) (string, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceLen, nonceLen+len(plain)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := aead.Seal(nonce, nonce, []byte(plain), []byte(aadPrefix+path))
	return SealedPrefix + base64.StdEncoding.EncodeToString(out), nil
}

// Open decrypts a sealed value of the field at path. A wrong key, a
// damaged value and a value moved from another field all fail.
func Open(key []byte, path, sealed string) (string, error) {
	raw, err := sealedBytes(sealed)
	if err != nil {
		return "", err
	}
	aead, err := newAEAD(key)
	if err != nil {
		return "", err
	}
	plain, err := aead.Open(nil, raw[:nonceLen], raw[nonceLen:], []byte(aadPrefix+path))
	if err != nil {
		return "", ErrSealedDamaged
	}
	return string(plain), nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != keyLen {
		return nil, errors.New("syncfile: key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func sealedBytes(v string) ([]byte, error) {
	b64, ok := strings.CutPrefix(v, SealedPrefix)
	if !ok {
		return nil, ErrSealedDamaged
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) < nonceLen+16 {
		return nil, ErrSealedDamaged
	}
	return raw, nil
}

func checkSealedFormat(v string) error {
	if _, err := sealedBytes(v); err != nil {
		return errors.New(i18n.T("加密的值应为 enc:v1:<base64>"))
	}
	return nil
}

// SealedValues lists the doc's sealed values by field path.
func SealedValues(doc Doc) map[string]string {
	out := map[string]string{}
	for _, s := range doc.Subscriptions {
		if IsSealed(s.URL) {
			out[SubscriptionPath(s.ID)] = s.URL
		}
	}
	if IsSealed(doc.WebDAVPassword) {
		out[WebDAVPasswordPath] = doc.WebDAVPassword
	}
	return out
}
