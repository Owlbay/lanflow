package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// WebDAV backup and restore (Settings.WebDAV). A backup is the settings
// export with the subscriptions included (settingsio.go) uploaded as
// lanflow-backup-YYYYMMDD-HHMMSS.json into the remote directory; restore
// downloads one and imports it through ImportSettings, so every part is
// validated like a manual import. The password lives in the system
// keychain through Secrets (wired by main) and, where the system has
// none, obfuscated in a 0600 file of the data directory.

const (
	backupPrefix     = "lanflow-backup-"
	backupSuffix     = ".json"
	backupTimeLayout = "20060102-150405"
	webdavSecretKey  = "webdav-password"
	webdavSecretFile = "webdav.secret"
	autoBackupRetry  = time.Hour // wait between failed automatic attempts
)

// SecretStore keeps small secrets in the system's credential store.
// main sets Secrets to MyGo's keychain module; nil (tests, or a build
// without it) means the file fallback. Get returns ErrSecretNotFound for
// a key never set, and an error for which errors.Is(err,
// errors.ErrUnsupported) holds where the system has no store.
type SecretStore interface {
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error
}

// Secrets is the credential store the service uses; see SecretStore.
var Secrets SecretStore

// ErrSecretNotFound is what SecretStore.Get returns for a missing key.
var ErrSecretNotFound = errors.New("secret not found")

// ---- password storage ----

func (s *Service) webdavSecretPath() string { return filepath.Join(s.opt.DataDir, webdavSecretFile) }

// loadPassword reads the password once: from the keychain, or the file
// fallback when the system has no keychain. The store kind is remembered
// for WebDAVInfo.
func (s *Service) loadPassword() (string, error) {
	st := feat2(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.pwLoaded {
		return st.password, nil
	}
	if Secrets != nil {
		v, err := Secrets.Get(webdavSecretKey)
		switch {
		case err == nil:
			st.password, st.pwLoaded, st.storeKind = string(v), true, model.PasswordStoreKeychain
			return st.password, nil
		case errors.Is(err, ErrSecretNotFound):
			st.pwLoaded, st.storeKind = true, model.PasswordStoreKeychain
			return "", nil
		case errors.Is(err, errors.ErrUnsupported):
			s.alog.Warnf("webdav: no system keychain (%v); the password is kept obfuscated in %s", err, webdavSecretFile)
		default:
			return "", fmt.Errorf(i18n.T("读取钥匙串失败：%v"), err)
		}
	}
	st.storeKind = model.PasswordStoreFile
	data, err := os.ReadFile(s.webdavSecretPath())
	if err != nil {
		if os.IsNotExist(err) {
			st.pwLoaded = true
			return "", nil
		}
		return "", fmt.Errorf(i18n.T("读取密码文件失败：%v"), err)
	}
	st.password, st.pwLoaded = deobfuscate(strings.TrimSpace(string(data))), true
	return st.password, nil
}

// SetWebDAVPassword implements API.SetWebDAVPassword: "" removes it.
func (s *Service) SetWebDAVPassword(pw string) (err error) {
	defer func() { s.logOp("set webdav password", err) }()
	if _, err := s.loadPassword(); err != nil {
		return err
	}
	st := feat2(s)
	st.mu.Lock()
	kind := st.storeKind
	st.mu.Unlock()
	if kind == model.PasswordStoreKeychain {
		if pw == "" {
			err = Secrets.Delete(webdavSecretKey)
		} else {
			err = Secrets.Set(webdavSecretKey, []byte(pw))
		}
		if err != nil {
			return fmt.Errorf(i18n.T("写入钥匙串失败：%v"), err)
		}
	} else {
		if pw == "" {
			if err := os.Remove(s.webdavSecretPath()); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf(i18n.T("删除密码文件失败：%v"), err)
			}
		} else if err := writeFileAtomic(s.webdavSecretPath(), []byte(obfuscate(pw)+"\n"), 0o600); err != nil {
			return fmt.Errorf(i18n.T("保存密码文件失败：%v"), err)
		}
	}
	st.mu.Lock()
	st.password, st.pwLoaded = pw, true
	st.mu.Unlock()
	s.em.emit(TopicSettings)
	return nil
}

// obfuscate hides pw from a casual look at the file: a random nonce and
// a SHA-256 keystream over it. It is not encryption (the key is in the
// file), which the settings UI says.
func obfuscate(pw string) string {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	return base64.StdEncoding.EncodeToString(append(nonce, xorStream(nonce, []byte(pw))...))
}

// deobfuscate reverses obfuscate; "" for anything unreadable.
func deobfuscate(s string) string {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(raw) < 16 {
		return ""
	}
	return string(xorStream(raw[:16], raw[16:]))
}

func xorStream(nonce, data []byte) []byte {
	out := make([]byte, len(data))
	var block [32]byte
	for i := range data {
		if i%32 == 0 {
			block = sha256.Sum256(append(append([]byte("lanflow-webdav:"), nonce...), byte(i/32)))
		}
		out[i] = data[i] ^ block[i%32]
	}
	return out
}

// ---- API ----

// WebDAVInfo implements API.WebDAVInfo (cheap read).
func (s *Service) WebDAVInfo() model.WebDAVInfo {
	s.mu.Lock()
	last := s.state.settings.WebDAV.LastBackupAt
	s.mu.Unlock()
	st := feat2(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.pwLoaded && !st.pwLoading {
		// The password is read lazily (the keychain may ask the user);
		// the read lands with TopicSettings and the next call sees it.
		st.pwLoading = true
		s.goBackground(func() {
			_, _ = s.loadPassword()
			st.mu.Lock()
			st.pwLoading = false
			st.mu.Unlock()
			s.em.emit(TopicSettings)
		})
	}
	kind := st.storeKind
	if kind == "" {
		if Secrets != nil {
			kind = model.PasswordStoreKeychain
		} else {
			kind = model.PasswordStoreFile
		}
	}
	return model.WebDAVInfo{
		PasswordStore: kind,
		HasPassword:   st.password != "",
		Busy:          st.busy,
		LastBackupAt:  last,
		LastError:     st.lastError,
	}
}

// webdavDir returns the remote directory of set.
func webdavDir(set model.WebDAVSettings) string {
	d := strings.Trim(strings.TrimSpace(set.Dir), "/")
	if d == "" {
		d = model.WebDAVDefaultDir
	}
	return d
}

// client builds the client for the current settings.
func (s *Service) webdavClient() (*webdavClient, model.WebDAVSettings, error) {
	s.mu.Lock()
	set := s.state.settings.WebDAV
	running := s.state.st.Core == model.CoreRunning
	port := s.state.settings.MixedPort
	s.mu.Unlock()
	if strings.TrimSpace(set.URL) == "" {
		return nil, set, errors.New(i18n.T("请先填写 WebDAV 服务器地址。"))
	}
	pw, err := s.loadPassword()
	if err != nil {
		return nil, set, err
	}
	// The server may only be reachable through the proxy (or only
	// directly); go through the running core like the other downloads,
	// which honours the rules, and directly without one.
	hc := s.d.httpDirect(webdavTimeout)
	if running {
		hc = s.d.httpVia(port, webdavTimeout)
	}
	c, err := newWebDAVClient(set.URL, set.User, pw, hc)
	if err != nil {
		return nil, set, err
	}
	return c, set, nil
}

// begin marks the backup state busy; it returns false when another
// operation runs.
func (s *Service) webdavBegin() (func(), bool) {
	st := feat2(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.busy {
		return nil, false
	}
	st.busy = true
	s.em.emit(TopicSettings)
	return func() {
		st.mu.Lock()
		st.busy = false
		st.mu.Unlock()
		s.em.emit(TopicSettings)
	}, true
}

var errWebDAVBusy = i18n.Error("正在进行其他备份操作，请稍候。")

// TestWebDAV implements API.TestWebDAV: connects, signs in and makes sure
// the remote directory exists (creating it).
func (s *Service) TestWebDAV() (err error) {
	defer func() { s.logOp("test webdav", err) }()
	c, set, err := s.webdavClient()
	if err != nil {
		return err
	}
	done, ok := s.webdavBegin()
	if !ok {
		return errWebDAVBusy
	}
	defer done()
	ctx, cancel := s.webdavCtx()
	defer cancel()
	if err := c.mkcol(ctx, webdavDir(set)); err != nil {
		return err
	}
	found, err := c.stat(ctx, webdavDir(set)+"/")
	if err != nil {
		return err
	}
	if !found {
		return errors.New(i18n.T("目录创建后仍不可见，服务器可能不支持 WebDAV。"))
	}
	return nil
}

func (s *Service) webdavCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.rootCtx, 5*time.Minute)
}

// backupDocument is the JSON of a backup: the export with subscriptions.
func (s *Service) backupDocument() ([]byte, error) {
	s.mu.Lock()
	set := copySettings(s.state.settings)
	rules := append([]model.CustomRule(nil), s.state.custom...)
	subs := append([]model.Subscription(nil), s.state.subs...)
	s.mu.Unlock()
	ex := buildExport(set, s.Overrides(), rules, subs, true)
	ex.ExportedAt = s.d.now()
	data, err := json.MarshalIndent(ex, "", "  ")
	if err != nil {
		return nil, fmt.Errorf(i18n.T("导出设置失败：%v"), err)
	}
	return append(data, '\n'), nil
}

// backupName is the file name of a backup taken at t.
func backupName(t time.Time) string {
	return backupPrefix + t.Format(backupTimeLayout) + backupSuffix
}

// isBackupName reports whether name is one of ours.
func isBackupName(name string) bool {
	return strings.HasPrefix(name, backupPrefix) && strings.HasSuffix(name, backupSuffix) && len(name) == len(backupPrefix)+len(backupTimeLayout)+len(backupSuffix)
}

// BackupNow implements API.BackupNow.
func (s *Service) BackupNow() error { return s.backup(false) }

// backup uploads one backup and prunes old ones; auto marks the
// scheduler's runs for the log and the error memory.
func (s *Service) backup(auto bool) (err error) {
	what := "backup"
	if auto {
		what = "automatic backup"
	}
	defer func() {
		s.logOp(what, err)
		if auto {
			st := feat2(s)
			st.mu.Lock()
			st.lastError = ""
			if err != nil {
				st.lastError = err.Error()
			}
			st.mu.Unlock()
		}
	}()
	c, set, err := s.webdavClient()
	if err != nil {
		return err
	}
	done, ok := s.webdavBegin()
	if !ok {
		return errWebDAVBusy
	}
	defer done()
	data, err := s.backupDocument()
	if err != nil {
		return err
	}
	ctx, cancel := s.webdavCtx()
	defer cancel()
	dir := webdavDir(set)
	if err := c.mkcol(ctx, dir); err != nil {
		return err
	}
	name := backupName(s.d.now())
	if err := c.put(ctx, dir+"/"+name, data); err != nil {
		return fmt.Errorf(i18n.T("上传备份失败：%v"), err)
	}
	s.alog.Infof("webdav: uploaded %s (%d bytes)", name, len(data))

	s.mu.Lock()
	s.state.settings.WebDAV.LastBackupAt = s.d.now()
	saved := copySettings(s.state.settings)
	s.mu.Unlock()
	s.em.emit(TopicSettings)
	if err := saveSettings(s.opt.DataDir, saved); err != nil {
		s.alog.Errorf("webdav: remember backup time: %v", err)
	}

	if set.Keep > 0 {
		if n, err := s.prune(ctx, c, dir, set.Keep); err != nil {
			s.alog.Warnf("webdav: pruning old backups: %v", err)
		} else if n > 0 {
			s.alog.Infof("webdav: pruned %d old backup(s), keeping %d", n, set.Keep)
		}
	}
	return nil
}

// prune deletes the backups beyond the newest keep; it returns how many.
func (s *Service) prune(ctx context.Context, c *webdavClient, dir string, keep int) (int, error) {
	entries, err := c.list(ctx, dir)
	if err != nil {
		return 0, err
	}
	var names []string
	for _, e := range entries {
		if !e.Dir && isBackupName(e.Name) {
			names = append(names, e.Name)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names))) // newest first (the name carries the time)
	n := 0
	for _, name := range names[min(keep, len(names)):] {
		if err := c.delete(ctx, dir+"/"+name); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ListBackups implements API.ListBackups: newest first.
func (s *Service) ListBackups() (out []model.WebDAVBackup, err error) {
	defer func() { s.logOp("list backups", err) }()
	c, set, err := s.webdavClient()
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.webdavCtx()
	defer cancel()
	dir := webdavDir(set)
	found, err := c.stat(ctx, dir+"/")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil // nothing backed up yet
	}
	entries, err := c.list(ctx, dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Dir || !isBackupName(e.Name) {
			continue
		}
		b := model.WebDAVBackup{Name: e.Name, Size: e.Size, Modified: e.Modified}
		if t, err := time.ParseInLocation(backupTimeLayout, strings.TrimSuffix(strings.TrimPrefix(e.Name, backupPrefix), backupSuffix), time.Local); err == nil {
			b.Modified = t
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

// RestoreBackup implements API.RestoreBackup: download, then import
// like a file the user picked.
func (s *Service) RestoreBackup(name string) (summary string, err error) {
	defer func() { s.logOp("restore backup "+name, err) }()
	if !isBackupName(name) {
		return "", errors.New(i18n.T("不是澜流的备份文件名。"))
	}
	c, set, err := s.webdavClient()
	if err != nil {
		return "", err
	}
	done, ok := s.webdavBegin()
	if !ok {
		return "", errWebDAVBusy
	}
	defer done()
	ctx, cancel := s.webdavCtx()
	defer cancel()
	data, err := c.get(ctx, webdavDir(set)+"/"+name)
	if err != nil {
		return "", err
	}
	dir := s.opt.CacheDir
	if dir == "" {
		dir = s.opt.DataDir
	}
	tmp := filepath.Join(dir, "restore-"+name)
	if err := writeFileAtomic(tmp, data, 0o600); err != nil {
		return "", fmt.Errorf(i18n.T("保存下载内容失败：%v"), err)
	}
	defer os.Remove(tmp)
	return s.ImportSettings(tmp)
}

// DeleteBackup implements API.DeleteBackup.
func (s *Service) DeleteBackup(name string) (err error) {
	defer func() { s.logOp("delete backup "+name, err) }()
	if !isBackupName(name) {
		return errors.New(i18n.T("不是澜流的备份文件名。"))
	}
	c, set, err := s.webdavClient()
	if err != nil {
		return err
	}
	ctx, cancel := s.webdavCtx()
	defer cancel()
	return c.delete(ctx, webdavDir(set)+"/"+name)
}

// ---- scheduler ----

// autoBackupInterval returns the interval of an AutoBackup value, 0 = off.
func autoBackupInterval(v string) time.Duration {
	switch v {
	case model.WebDAVDaily:
		return 24 * time.Hour
	case model.WebDAVWeekly:
		return 7 * 24 * time.Hour
	}
	return 0
}

// autoBackupDue reports whether an automatic backup should run at now.
func autoBackupDue(set model.WebDAVSettings, lastAttempt, now time.Time) bool {
	iv := autoBackupInterval(set.AutoBackup)
	if iv == 0 || strings.TrimSpace(set.URL) == "" {
		return false
	}
	if !lastAttempt.IsZero() && now.Sub(lastAttempt) < autoBackupRetry {
		return false
	}
	return set.LastBackupAt.IsZero() || now.Sub(set.LastBackupAt) >= iv
}

// webdavTick runs from the system watcher (watch.go): it starts an
// automatic backup in the background when one is due.
func (s *Service) webdavTick() {
	s.mu.Lock()
	set := s.state.settings.WebDAV
	s.mu.Unlock()
	st := feat2(s)
	st.mu.Lock()
	due := !st.busy && autoBackupDue(set, st.lastAttempt, s.d.now())
	if due {
		st.lastAttempt = s.d.now()
	}
	st.mu.Unlock()
	if !due {
		return
	}
	s.alog.Infof("webdav: automatic %s backup due", set.AutoBackup)
	s.goBackground(func() {
		if err := s.backup(true); err != nil {
			s.notify(Notice{Kind: "error", Title: i18n.T("自动备份到 WebDAV 失败"), Detail: err.Error()})
		}
	})
}
