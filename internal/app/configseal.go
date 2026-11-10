package app

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/syncfile"
)

// Secrets sealed with the sync passphrase (docs/config-sync.md 用口令加密
// 链接; the format is syncfile/seal.go). With it on, lanflow.yaml carries
// the subscription links and the WebDAV password as enc:v1: values
// instead of keychain references, so a device whose keychain does not
// have them (Windows ↔ Mac, or a Mac without iCloud Keychain) gets them
// from the file once the user enters the passphrase there.
//
// The passphrase itself is never stored: the key derived from it (with
// the file's salt) goes into this machine's keychain as sync-key, so
// later starts need no passphrase. The file's encryption block is the
// source of truth: whether encryption is on, and which key opens it. A
// key that no longer opens the check value means the passphrase was
// changed on another device, and the user is asked for the new one.
//
// A device without a valid key (locked) still loads everything else; it
// keeps the links it has, and when it rewrites the file it passes the
// sealed values through unchanged so it never drops another device's
// secrets. A value is sealed again only when its plaintext changes: the
// nonce is random, and the canonical writer compares texts.

const syncKeySecret = "sync-key" // keychain key of the derived key

// ErrSyncLocked is what changing the passphrase says on a device that
// cannot open the file's secrets.
var ErrSyncLocked = i18n.Error("请先输入当前的同步口令解锁，再修改口令。")

// sealState is the encryption state of the sync file; under its own mu
// (a leaf, never held across I/O or the KDF).
type sealState struct {
	mu      sync.Mutex
	primed  bool                  // header read from the file of this run's folder
	header  *syncfile.Encryption  // the file's encryption block, nil = off
	key     []byte                // the derived key, when it opens header
	stale   bool                  // a stored key that does not open header (passphrase changed elsewhere)
	values  map[string]sealedItem // field path → the sealed value in the file and its plaintext
	keyRead bool                  // sync-key read from the keychain this run
	stored  *storedKey            // what the keychain had
}

// sealedItem is one sealed value; plain is "" while it cannot be opened.
type sealedItem struct {
	sealed, plain string
}

// storedKey is the keychain entry: the key and the salt it belongs to.
type storedKey struct {
	Salt string `json:"salt"`
	Key  string `json:"key"` // base64
}

// ---- keychain ----

// readStoredKey reads sync-key once per run (nil when there is none).
func (s *Service) readStoredKey() *storedKey {
	ss := &s.cs.seal
	ss.mu.Lock()
	if ss.keyRead {
		k := ss.stored
		ss.mu.Unlock()
		return k
	}
	ss.mu.Unlock()
	var k *storedKey
	if st := s.secretStore(); st != nil {
		v, err := st.Get(syncKeySecret)
		switch {
		case err == nil:
			var sk storedKey
			if json.Unmarshal(v, &sk) == nil && sk.Key != "" {
				k = &sk
			}
		case !errors.Is(err, ErrSecretNotFound):
			s.alog.Warnf("config sync: keychain read %s: %v", syncKeySecret, err)
		}
	}
	ss.mu.Lock()
	ss.keyRead, ss.stored = true, k
	ss.mu.Unlock()
	return k
}

// saveStoredKey puts the derived key into the keychain.
func (s *Service) saveStoredKey(salt string, key []byte) error {
	st := s.secretStore()
	if st == nil {
		return errors.New(i18n.T("本机没有可用的钥匙串，无法保存同步口令派生的密钥。"))
	}
	sk := storedKey{Salt: salt, Key: base64.StdEncoding.EncodeToString(key)}
	data, _ := json.Marshal(sk)
	if err := st.Set(syncKeySecret, data); err != nil {
		return fmt.Errorf(i18n.T("写入钥匙串失败：%v"), err)
	}
	ss := &s.cs.seal
	ss.mu.Lock()
	ss.keyRead, ss.stored = true, &sk
	ss.mu.Unlock()
	return nil
}

func (s *Service) deleteStoredKey() {
	if st := s.secretStore(); st != nil {
		if err := st.Delete(syncKeySecret); err != nil && !errors.Is(err, ErrSecretNotFound) {
			s.alog.Warnf("config sync: keychain delete %s: %v", syncKeySecret, err)
		}
	}
	ss := &s.cs.seal
	ss.mu.Lock()
	ss.keyRead, ss.stored = true, nil
	ss.mu.Unlock()
}

// ---- state ----

// keyFor returns this machine's key when it opens doc's encryption
// block; stale is true for a stored key that does not (the passphrase
// was changed on another device, or encryption turned off and on again).
func (s *Service) keyFor(doc syncfile.Doc) (key []byte, stale bool) {
	if doc.Encryption == nil {
		return nil, false
	}
	sk := s.readStoredKey()
	if sk == nil {
		return nil, false
	}
	k, err := base64.StdEncoding.DecodeString(sk.Key)
	if err == nil && sk.Salt == doc.Encryption.Salt && doc.Encryption.Verify(k) {
		return k, false
	}
	return nil, true
}

// primeSeal takes the encryption state of the sync file's doc: its
// block, its sealed values, and whether this machine's key opens them.
func (s *Service) primeSeal(doc syncfile.Doc) {
	key, stale := s.keyFor(doc)
	values := map[string]sealedItem{}
	for path, v := range syncfile.SealedValues(doc) {
		it := sealedItem{sealed: v}
		if key != nil {
			if p, err := syncfile.Open(key, path, v); err == nil {
				it.plain = p
			} else {
				s.alog.Warnf("config sync: the sealed value of %s does not open: %v", path, err)
			}
		}
		values[path] = it
	}
	var header *syncfile.Encryption
	if doc.Encryption != nil {
		h := *doc.Encryption
		header = &h
	}
	ss := &s.cs.seal
	ss.mu.Lock()
	ss.primed, ss.header, ss.key, ss.stale, ss.values = true, header, key, stale, values
	ss.mu.Unlock()
}

// resetSeal forgets the file's state (sync off, or another folder).
func (s *Service) resetSeal() {
	ss := &s.cs.seal
	ss.mu.Lock()
	ss.primed, ss.header, ss.key, ss.stale, ss.values = false, nil, nil, false, nil
	ss.mu.Unlock()
}

// sealOn reports whether the sync file is sealed.
func (s *Service) sealOn() bool {
	ss := &s.cs.seal
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.header != nil
}

// sealPrimed reports whether the file's encryption state is known.
func (s *Service) sealPrimed() bool {
	ss := &s.cs.seal
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.primed
}

// unsealDoc returns doc with the sealed values this machine can open
// replaced by their plaintext and the others by "" (the local value
// stays, as for a keychain reference this machine lacks), and the WebDAV
// password it opened. A file with another salt than this machine's key
// opens nothing.
func (s *Service) unsealDoc(doc syncfile.Doc) (out syncfile.Doc, webdav string) {
	key, _ := s.keyFor(doc)
	open := func(path, v string) string {
		if key == nil {
			return ""
		}
		p, err := syncfile.Open(key, path, v)
		if err != nil {
			return ""
		}
		return p
	}
	out = doc
	out.Subscriptions = append([]syncfile.Subscription(nil), doc.Subscriptions...)
	for i, sub := range out.Subscriptions {
		if syncfile.IsSealed(sub.URL) {
			out.Subscriptions[i].URL = open(syncfile.SubscriptionPath(sub.ID), sub.URL)
		}
	}
	if syncfile.IsSealed(doc.WebDAVPassword) {
		webdav = open(syncfile.WebDAVPasswordPath, doc.WebDAVPassword)
		out.WebDAVPassword = ""
	}
	return out, webdav
}

// adoptWebDAVPassword stores a password opened from the file when this
// machine has none or another one.
func (s *Service) adoptWebDAVPassword(pw string) {
	if pw == "" {
		return
	}
	if cur, err := s.loadPassword(); err == nil && cur == pw {
		return
	}
	if err := s.SetWebDAVPassword(pw); err != nil {
		s.alog.Warnf("config sync: store the WebDAV password from the file: %v", err)
	}
}

// sealDoc turns the references of a document about to be written into
// sealed values when the file is encrypted. subs are this machine's
// subscriptions (for the links), webdav its WebDAV password.
func (s *Service) sealDoc(doc *syncfile.Doc, subs []model.Subscription, webdav string) {
	ss := &s.cs.seal
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.header == nil {
		return
	}
	h := *ss.header
	doc.Encryption = &h
	if ss.values == nil {
		ss.values = map[string]sealedItem{}
	}
	links := map[string]string{}
	for _, sub := range subs {
		links[sub.ID] = sub.URL
	}
	for i, e := range doc.Subscriptions {
		doc.Subscriptions[i].URL = ss.sealLocked(syncfile.SubscriptionPath(e.ID), links[e.ID], e.URL)
	}
	if v := ss.sealLocked(syncfile.WebDAVPasswordPath, webdav, doc.WebDAVPassword); v != "" {
		doc.WebDAVPassword = v
	}
	// values of entries no longer in the file are dropped
	keep := map[string]bool{syncfile.WebDAVPasswordPath: doc.WebDAVPassword != ""}
	for _, e := range doc.Subscriptions {
		keep[syncfile.SubscriptionPath(e.ID)] = true
	}
	for p := range ss.values {
		if !keep[p] {
			delete(ss.values, p)
		}
	}
}

// sealLocked returns what the file holds for the secret at path:
// the previous sealed value while its plaintext is unchanged (or unknown:
// a locked device passes it through), a new one for a changed plaintext,
// fallback (the keychain reference) when it cannot be sealed. ss.mu held.
func (ss *sealState) sealLocked(path, plain, fallback string) string {
	it, had := ss.values[path]
	switch {
	case had && (it.plain == plain || plain == "" || (it.plain == "" && ss.key == nil)):
		// unchanged, or this machine lacks the plaintext (never drop
		// another device's secret), or locked (cannot seal the change)
		return it.sealed
	case plain == "":
		return fallback
	case ss.key == nil:
		return fallback // locked: a new link stays a keychain reference until unlocked
	}
	v, err := syncfile.Seal(ss.key, path, plain)
	if err != nil {
		return fallback
	}
	ss.values[path] = sealedItem{sealed: v, plain: plain}
	return v
}

// sealStatus fills the encryption part of the status.
func (s *Service) sealStatus(st *model.ConfigSyncStatus) {
	ss := &s.cs.seal
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.header == nil {
		return
	}
	st.Encrypted = true
	st.Locked = ss.key == nil
	st.PassphraseChanged = st.Locked && ss.stale
	if st.Locked {
		st.LockedSecrets = len(ss.values)
	}
}

// ensureSealPrimed reads the encryption block of the sync file at path
// when this run has not seen it yet (the file is this machine's own
// write, so it was not loaded). runMu held.
func (s *Service) ensureSealPrimed(data []byte) {
	if s.sealPrimed() {
		return
	}
	doc, err := syncfile.Decode(data, defaultSettings())
	if err != nil {
		return // loading reports it
	}
	s.primeSeal(doc)
}

// ---- API ----

// syncFileForSeal returns the folder and the current sync file; runMu held.
func (s *Service) syncFileForSeal() (dir string, doc syncfile.Doc, data []byte, err error) {
	dir = s.syncDir(s.Settings().Sync)
	if dir == "" {
		return "", doc, nil, errors.New(i18n.T("配置同步未开启。"))
	}
	data, err = os.ReadFile(filepath.Join(dir, syncfile.FileName))
	if err != nil {
		if os.IsNotExist(err) {
			return dir, doc, nil, nil
		}
		return "", doc, nil, fmt.Errorf(i18n.T("读取同步文件失败：%v"), err)
	}
	doc, err = syncfile.Decode(data, defaultSettings())
	if err != nil {
		return "", doc, nil, fmt.Errorf(i18n.T("同步文件无效：%v"), err)
	}
	return dir, doc, data, nil
}

// SetSyncPassphrase implements API.SetSyncPassphrase: a passphrase turns
// sealing on, or changes the passphrase (every secret is sealed again
// under a new salt and the file rewritten atomically; other devices are
// asked for the new one); "" turns it off, the file going back to
// keychain references.
func (s *Service) SetSyncPassphrase(passphrase string) (err error) {
	op := "set sync passphrase"
	if passphrase == "" {
		op = "turn off sync passphrase"
	}
	defer func() { s.logOp(op, err) }()
	s.cs.runMu.Lock()
	defer s.cs.runMu.Unlock()
	dir, doc, data, err := s.syncFileForSeal()
	if err != nil {
		return err
	}
	if data != nil && !s.sealPrimed() {
		s.primeSeal(doc)
	}
	if passphrase == "" {
		s.resetSeal()
		s.deleteStoredKey()
		s.markSealedPrimed()
	} else {
		ss := &s.cs.seal
		ss.mu.Lock()
		locked := ss.header != nil && ss.key == nil
		ss.mu.Unlock()
		if locked {
			return ErrSyncLocked
		}
		enc, key, err := syncfile.NewEncryption(passphrase)
		if err != nil {
			return err
		}
		if err := s.saveStoredKey(enc.Salt, key); err != nil {
			return err
		}
		ss.mu.Lock()
		ss.primed, ss.header, ss.key, ss.stale, ss.values = true, &enc, key, false, map[string]sealedItem{}
		ss.mu.Unlock()
	}
	s.cs.mu.Lock()
	s.cs.dirty = true
	s.cs.mu.Unlock()
	s.writeSyncFile(dir)
	s.em.emit(TopicSettings)
	if st := s.ConfigSyncStatus(); st.State == model.SyncStateError && st.Error != "" {
		return errors.New(st.Error)
	}
	return nil
}

// markSealedPrimed records that the file's state is known (off).
func (s *Service) markSealedPrimed() {
	ss := &s.cs.seal
	ss.mu.Lock()
	ss.primed = true
	ss.mu.Unlock()
}

// UnlockSync implements API.UnlockSync: the passphrase of the sync
// file's secrets on this device. Right, the derived key goes into the
// keychain and the file is loaded again with its secrets; wrong, it
// returns syncfile.ErrWrongPassphrase and changes nothing.
func (s *Service) UnlockSync(passphrase string) (err error) {
	defer func() { s.logOp("unlock sync", err) }()
	s.cs.runMu.Lock()
	defer s.cs.runMu.Unlock()
	dir, doc, data, err := s.syncFileForSeal()
	if err != nil {
		return err
	}
	if doc.Encryption == nil {
		return errors.New(i18n.T("同步文件中没有用口令加密的链接。"))
	}
	key, err := doc.Encryption.Unlock(passphrase)
	if err != nil {
		return err
	}
	if err := s.saveStoredKey(doc.Encryption.Salt, key); err != nil {
		return err
	}
	s.primeSeal(doc)
	path := filepath.Join(dir, syncfile.FileName)
	s.loadExternal(path, data, syncfile.Hash(data))
	if st := s.ConfigSyncStatus(); st.State == model.SyncStateError && st.Error != "" {
		return errors.New(st.Error)
	}
	return nil
}

// ForgetSyncPassphrase implements API.ForgetSyncPassphrase: removes the
// derived key from this machine's keychain. Links already here stay;
// new or changed ones from the file wait for the passphrase again.
func (s *Service) ForgetSyncPassphrase() (err error) {
	defer func() { s.logOp("forget sync passphrase", err) }()
	s.cs.runMu.Lock()
	defer s.cs.runMu.Unlock()
	s.deleteStoredKey()
	ss := &s.cs.seal
	ss.mu.Lock()
	ss.key, ss.stale = nil, false
	for p, it := range ss.values {
		ss.values[p] = sealedItem{sealed: it.sealed}
	}
	ss.mu.Unlock()
	s.em.emit(TopicSettings)
	return nil
}
