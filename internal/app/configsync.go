package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"lanflow/internal/i18n"
	"lanflow/internal/model"
	"lanflow/internal/syncfile"
)

// Configuration sync (docs/config-sync.md). With Settings.Sync on, the
// configuration lives in Dir/lanflow.yaml too, a folder that iCloud
// Drive, OneDrive or another program syncs between machines:
//
//   - local changes (TopicSettings / TopicSubs / TopicRules) mark the file
//     dirty; the loop rewrites it once a second at most, and only when its
//     canonical text changed;
//   - the loop polls the folder; a change that has been stable for the
//     debounce time is read, and recognised by its hash when it is the
//     file this machine wrote or already processed;
//   - another change is decoded, planned, checked with `mihomo -t` and
//     loaded (configapply.go); a refused one leaves everything as it was,
//     and the status (and a notice) say why, with the line;
//   - an iCloud placeholder (.lanflow.yaml.icloud) asks iCloud to download
//     the file (brctl download) and waits;
//   - conflict copies ("lanflow 2.yaml") are listed for the user to pick
//     from (ResolveConfigConflict); nothing is deleted automatically.
//
// Lock order: runMu → opMu → settingsMu → mu; cs.mu is a leaf, never held
// across I/O.

const (
	syncPoll          = time.Second
	syncDebounce      = 1500 * time.Millisecond // a change must be this old (and unchanged) before it is read
	syncDownloadRetry = 15 * time.Second        // between iCloud download requests
	syncStateFile     = "sync-state.json"       // last hash per file, so a restart knows what changed meanwhile
	syncBackupDir     = "sync-backups"          // files replaced by an explicit choice
)

// ErrConfigSyncChoice is what SetConfigSync returns when the folder
// already has a lanflow.yaml that differs from this machine's
// configuration: the user picks model.SyncUseFile or model.SyncUseLocal.
var ErrConfigSyncChoice = i18n.Error("同步文件夹中已有不同的 lanflow.yaml：请选择使用文件中的配置，还是用本机配置覆盖文件。")

// configSyncState is the in-memory state of configuration sync.
type configSyncState struct {
	// secrets replaces Secrets for this service (tests); nil = Secrets.
	secrets SecretStore
	links   linkSecrets // links this run put into the keychain (configsecrets.go)
	seal    sealState   // secrets sealed with the sync passphrase (configseal.go)

	// test hooks: home replaces the user's home directory, download the
	// iCloud download request, debounce the stable time (0 = syncDebounce).
	home     string
	download func(path string) error
	debounce time.Duration
	paused   atomic.Bool // the loop skips its steps (tests drive syncStep)

	runMu sync.Mutex // one sync step, enable or apply at a time

	mu        sync.Mutex
	loaded    bool              // persisted hashes read
	dirty     bool              // a local change waits to be written (set at start: compare once)
	hashes    map[string]string // path → hash of the content last written or processed there
	canon     []byte            // the canonical text last written or loaded
	snap      syncfile.Snapshot // the folder as last seen
	snapDir   string
	changedAt time.Time // when snap last changed
	askedAt   time.Time // last iCloud download request
	notified  string    // the error last shown as a notice
	conflicts string    // the conflict set last shown as a notice
	st        model.ConfigSyncStatus
	icloudDir string    // iCloudDir's cached answer
	icloudAt  time.Time // when it was looked up
}

// ---- folders ----

func (s *Service) syncHome() string {
	if s.cs.home != "" {
		return s.cs.home
	}
	h, _ := os.UserHomeDir()
	return h
}

// iCloudDir is the folder of the iCloud mode, "" when iCloud Drive is
// not available here (not macOS, or no iCloud Drive folder). The answer
// is cached for a few seconds: views read it on every frame.
func (s *Service) iCloudDir() string {
	if runtime.GOOS != "darwin" && s.cs.home == "" {
		return ""
	}
	now := time.Now()
	s.cs.mu.Lock()
	if !s.cs.icloudAt.IsZero() && now.Sub(s.cs.icloudAt) < 5*time.Second {
		d := s.cs.icloudDir
		s.cs.mu.Unlock()
		return d
	}
	s.cs.mu.Unlock()
	dir := ""
	drive := syncfile.ICloudDrive(s.syncHome())
	if fi, err := os.Stat(drive); err == nil && fi.IsDir() {
		dir = filepath.Join(drive, syncfile.ICloudSubdir)
	}
	s.cs.mu.Lock()
	s.cs.icloudDir, s.cs.icloudAt = dir, now
	s.cs.mu.Unlock()
	return dir
}

// syncDir is the folder set syncs to, "" when off or unavailable.
func (s *Service) syncDir(set model.ConfigSyncSettings) string {
	switch set.Mode {
	case model.SyncICloud:
		return s.iCloudDir()
	case model.SyncFolder:
		return set.Dir
	}
	return ""
}

// ---- persisted hashes ----

type syncPersist struct {
	Hashes map[string]string `json:"hashes"`
}

func (s *Service) syncStatePath() string { return filepath.Join(s.opt.DataDir, syncStateFile) }

// loadSyncHashes reads the hashes once; cs.mu held.
func (s *Service) loadSyncHashesLocked() {
	if s.cs.loaded {
		return
	}
	s.cs.loaded = true
	s.cs.hashes = map[string]string{}
	data, err := os.ReadFile(s.syncStatePath())
	if err != nil {
		return
	}
	var p syncPersist
	if json.Unmarshal(data, &p) == nil && p.Hashes != nil {
		s.cs.hashes = p.Hashes
	}
}

// rememberHash records the content last written or processed at path.
func (s *Service) rememberHash(path, hash string) {
	s.cs.mu.Lock()
	s.loadSyncHashesLocked()
	s.cs.hashes[path] = hash
	data, _ := json.Marshal(syncPersist{Hashes: s.cs.hashes})
	s.cs.mu.Unlock()
	if err := writeFileAtomic(s.syncStatePath(), data, 0o600); err != nil {
		s.alog.Warnf("config sync: save state: %v", err)
	}
}

func (s *Service) knownHash(path string) string {
	s.cs.mu.Lock()
	defer s.cs.mu.Unlock()
	s.loadSyncHashesLocked()
	return s.cs.hashes[path]
}

// ---- loop ----

// configSyncLoop runs the sync steps until shutdown.
func (s *Service) configSyncLoop() {
	off := s.em.add(func(t Topic) {
		switch t {
		case TopicSettings, TopicSubs, TopicRules:
			s.cs.mu.Lock()
			s.cs.dirty = true
			s.cs.mu.Unlock()
		}
	})
	defer off()
	s.cs.mu.Lock()
	s.cs.dirty = true // compare the file with this machine once at start
	s.cs.mu.Unlock()
	t := time.NewTicker(syncPoll)
	defer t.Stop()
	for {
		select {
		case <-s.rootCtx.Done():
			return
		case <-t.C:
			if !s.cs.paused.Load() {
				s.syncStep()
			}
		}
	}
}

// syncStep looks at the folder once: loads an external change, asks
// iCloud for a missing file, writes a local change.
func (s *Service) syncStep() {
	set := s.Settings().Sync
	dir := s.syncDir(set)
	if dir == "" {
		s.setSyncStatus(func(st *model.ConfigSyncStatus) { *st = model.ConfigSyncStatus{} })
		return
	}
	s.cs.runMu.Lock()
	defer s.cs.runMu.Unlock()
	path := filepath.Join(dir, syncfile.FileName)
	s.cs.mu.Lock()
	moved := s.cs.snapDir != "" && s.cs.snapDir != dir
	s.cs.mu.Unlock()
	if moved {
		s.resetSeal() // another folder: its file says whether it is sealed
	}
	now := s.d.now()
	snap := syncfile.Look(dir)

	s.cs.mu.Lock()
	if s.cs.snapDir != dir || !snap.Same(s.cs.snap) {
		s.cs.snap, s.cs.snapDir, s.cs.changedAt = snap, dir, now
	}
	debounce := s.cs.debounce
	if debounce == 0 {
		debounce = syncDebounce
	}
	stable := now.Sub(s.cs.changedAt) >= debounce
	dirty := s.cs.dirty
	s.cs.mu.Unlock()
	s.noteConflicts(snap.Conflicts)

	switch {
	case snap.Placeholder:
		s.askDownload(path)
		s.setSyncStatus(func(st *model.ConfigSyncStatus) { st.State = model.SyncStateWaiting })
		return
	case !stable:
		return // still changing (another program writing it): look again later
	case snap.Exists:
		data, err := os.ReadFile(path)
		if err != nil {
			s.syncFailed(path, "", fmt.Errorf(i18n.T("读取同步文件失败：%v"), err), 0)
			return
		}
		if h := syncfile.Hash(data); h != s.knownHash(path) {
			s.loadExternal(path, data, h)
			return
		}
		// this machine's own file: learn whether it is sealed before
		// writing over it (the first step after a start)
		s.ensureSealPrimed(data)
	default:
		dirty = true // no file (deleted, or never written): write this machine's
		if !s.sealPrimed() {
			s.markSealedPrimed()
		}
	}
	if dirty {
		s.writeSyncFile(dir)
	}
}

// loadExternal processes a content of the file this machine has not
// seen: loads it, or records why not. Either way the hash is
// remembered, so the same content is not tried again.
func (s *Service) loadExternal(path string, data []byte, hash string) {
	s.alog.Infof("config sync: %s changed (%s)", path, hash[:12])
	doc, warns, err := syncfile.DecodeWarn(data, defaultSettings())
	if err != nil {
		s.rememberHash(path, hash)
		line := 0
		var se *syncfile.Error
		if errors.As(err, &se) {
			line = se.Line
		}
		s.syncFailed(path, hash, err, line)
		return
	}
	for _, w := range warns {
		s.alog.Warnf("config sync: %s: %v", path, w)
	}
	plain, webdav := s.unsealDoc(doc)
	res, err := s.applyDoc(plain, false)
	s.rememberHash(path, hash)
	if err != nil {
		line := 0
		var de *docError
		if errors.As(err, &de) {
			line = syncfile.LineOf(data, de.path)
		}
		s.syncFailed(path, hash, err, line)
		return
	}
	s.primeSeal(doc)
	s.adoptWebDAVPassword(webdav)
	now := s.d.now()
	missing := s.missingSecrets(doc)
	s.cs.mu.Lock()
	s.cs.canon = syncfile.Encode(doc)
	// a canonical rewrite is due when the file is not (comments, plain
	// links, settings out of range): the writer compares texts
	s.cs.dirty = true
	s.cs.notified = ""
	s.cs.mu.Unlock()
	s.setSyncStatus(func(st *model.ConfigSyncStatus) {
		st.State, st.Error, st.ErrorLine = model.SyncStateOK, "", 0
		st.MissingSecrets, st.WebDAVPasswordMissing = missing.subs, missing.webdav
		if res.Applied {
			st.LastAppliedAt, st.Summary = now, res.Summary
		}
	})
	if res.Applied {
		s.alog.Infof("config sync: loaded %s: %s", path, res.Summary)
		s.notify(Notice{Kind: "toast", Title: i18n.T("已加载同步的配置"), Detail: res.Summary})
	}
}

// syncFailed records a refused change (or an I/O failure) for the status
// and shows it once as a notice.
func (s *Service) syncFailed(path, hash string, err error, line int) {
	s.alog.Warnf("config sync: %s refused: %v", path, err)
	msg := err.Error()
	s.setSyncStatus(func(st *model.ConfigSyncStatus) {
		st.State, st.Error, st.ErrorLine = model.SyncStateError, msg, line
	})
	s.cs.mu.Lock()
	again := s.cs.notified == msg
	s.cs.notified = msg
	s.cs.mu.Unlock()
	if !again {
		s.notify(Notice{Kind: "error", Title: i18n.T("同步文件未加载，配置保持不变"), Detail: msg})
	}
}

type missingInfo struct {
	subs   []string
	webdav bool
}

// missingSecrets lists what the file references that this machine's
// keychain lacks.
func (s *Service) missingSecrets(doc syncfile.Doc) missingInfo {
	var m missingInfo
	for _, sub := range s.d.store.List() {
		if sub.Source == urlSubscriptionK && sub.URL == "" {
			m.subs = append(m.subs, sub.Name)
		}
	}
	if doc.WebDAVPassword != "" && !s.WebDAVInfo().HasPassword && s.resolveRef(doc.WebDAVPassword) == "" {
		m.webdav = true
	}
	return m
}

// writeSyncFile writes this machine's configuration when its canonical
// text changed since the last write or load. runMu held.
func (s *Service) writeSyncFile(dir string) {
	path := filepath.Join(dir, syncfile.FileName)
	st := s.snapshotLocal()
	if err := s.storeLinkSecrets(st.subs); err != nil {
		s.alog.Warnf("config sync: keychain: %v (the file keeps keychain references; this machine keeps its links)", err)
	}
	doc := buildDoc(st, linksKeychain)
	if s.WebDAVInfo().HasPassword {
		doc.WebDAVPassword = syncfile.WebDAVPasswordRef
	}
	if s.sealOn() {
		pw := ""
		if doc.WebDAVPassword != "" {
			pw, _ = s.loadPassword()
		}
		s.sealDoc(&doc, st.subs, pw)
	}
	data := syncfile.Encode(doc)
	s.cs.mu.Lock()
	s.cs.dirty = false
	same := bytes.Equal(data, s.cs.canon) && s.cs.snap.Exists
	s.cs.mu.Unlock()
	if same {
		return
	}
	h := syncfile.Hash(data)
	if old, err := os.ReadFile(path); err == nil {
		if syncfile.Hash(old) == h {
			// already there (a restart, or another machine wrote the same)
			s.rememberHash(path, h)
			s.cs.mu.Lock()
			s.cs.canon = data
			s.cs.mu.Unlock()
			return
		}
		s.cs.mu.Lock()
		ours := bytes.Equal(old, s.cs.canon)
		s.cs.mu.Unlock()
		if !ours {
			// a version this machine did not write (a refused change, or
			// a hand-edited file): keep it before replacing it
			if err := s.backupSyncFile(path, old); err != nil {
				s.alog.Warnf("config sync: %v", err)
			}
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.syncFailed(path, "", fmt.Errorf(i18n.T("无法创建同步文件夹：%v"), err), 0)
		return
	}
	if err := writeFileAtomic(path, data, 0o600); err != nil {
		s.syncFailed(path, "", fmt.Errorf(i18n.T("写入同步文件失败：%v"), err), 0)
		return
	}
	s.rememberHash(path, h)
	now := s.d.now()
	snap := syncfile.Look(dir)
	s.cs.mu.Lock()
	s.cs.canon = data
	s.cs.snap, s.cs.snapDir, s.cs.changedAt = snap, dir, time.Time{}
	s.cs.mu.Unlock()
	s.setSyncStatus(func(st *model.ConfigSyncStatus) {
		st.LastWrittenAt = now
		if st.State != model.SyncStateError || st.Error == "" {
			st.State = model.SyncStateOK
		}
	})
	s.alog.Infof("config sync: wrote %s (%d bytes)", path, len(data))
}

// askDownload asks iCloud to download the file behind a placeholder,
// at most every syncDownloadRetry.
func (s *Service) askDownload(path string) {
	now := s.d.now()
	s.cs.mu.Lock()
	due := s.cs.askedAt.IsZero() || now.Sub(s.cs.askedAt) >= syncDownloadRetry
	if due {
		s.cs.askedAt = now
	}
	dl := s.cs.download
	s.cs.mu.Unlock()
	if !due {
		return
	}
	if dl == nil {
		dl = brctlDownload
	}
	s.alog.Infof("config sync: %s is not downloaded yet; asking iCloud", path)
	if err := dl(path); err != nil {
		s.alog.Warnf("config sync: iCloud download %s: %v", path, err)
	}
}

// brctlDownload asks iCloud Drive to download path (macOS's brctl; the
// download continues after it returns).
func brctlDownload(path string) error {
	if runtime.GOOS != "darwin" {
		return errors.ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/brctl", "download", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// noteConflicts records the conflict copies and shows a notice when a
// new set appears.
func (s *Service) noteConflicts(names []string) {
	key := fmt.Sprint(names)
	s.cs.mu.Lock()
	changed := s.cs.conflicts != key
	s.cs.conflicts = key
	s.cs.mu.Unlock()
	s.setSyncStatus(func(st *model.ConfigSyncStatus) { st.Conflicts = append([]string(nil), names...) })
	if changed && len(names) > 0 {
		s.notify(Notice{Kind: "error", Title: i18n.T("同步文件有冲突副本"),
			Detail: i18n.Tf("同步文件夹里有 %d 个冲突副本（%s）。请在「设置 › 常规 › 配置同步」中选择保留哪一个；澜流不会自动删除任何副本。", len(names), names[0])})
	}
}

// setSyncStatus changes the cached status and emits TopicSettings when
// it changed.
func (s *Service) setSyncStatus(fn func(*model.ConfigSyncStatus)) {
	s.cs.mu.Lock()
	before := fmt.Sprintf("%+v", s.cs.st)
	fn(&s.cs.st)
	changed := before != fmt.Sprintf("%+v", s.cs.st)
	s.cs.mu.Unlock()
	if changed {
		s.em.emit(TopicSettings)
	}
}

// ---- API ----

// ConfigSyncStatus implements API.ConfigSyncStatus (cheap read).
func (s *Service) ConfigSyncStatus() model.ConfigSyncStatus {
	set := s.Settings().Sync
	s.cs.mu.Lock()
	st := s.cs.st
	st.Conflicts = append([]string(nil), st.Conflicts...)
	st.MissingSecrets = append([]string(nil), st.MissingSecrets...)
	s.cs.mu.Unlock()
	st.Mode = set.Mode
	st.ICloudDir = s.iCloudDir()
	st.ICloudAvailable = st.ICloudDir != ""
	st.Dir = s.syncDir(set)
	if st.Dir == "" {
		st.Mode, st.State = model.SyncOff, model.SyncStateOff
		return model.ConfigSyncStatus{State: model.SyncStateOff, ICloudAvailable: st.ICloudAvailable, ICloudDir: st.ICloudDir, Mode: set.Mode}
	}
	st.Path = filepath.Join(st.Dir, syncfile.FileName)
	if st.State == "" {
		st.State = model.SyncStateOK
	}
	s.sealStatus(&st)
	return st
}

// SetConfigSync implements API.SetConfigSync: turns sync on (mode
// model.SyncICloud, or model.SyncFolder with dir) or off. When the folder
// has a lanflow.yaml that differs from this machine's configuration,
// choice says which wins (model.SyncUseFile / SyncUseLocal); without one
// it returns ErrConfigSyncChoice and changes nothing.
func (s *Service) SetConfigSync(mode, dir, choice string) (err error) {
	defer func() { s.logOp("set config sync "+mode, err) }()
	want := model.ConfigSyncSettings{Mode: mode}
	switch mode {
	case model.SyncOff:
		err := s.updateSettings(func(set *model.Settings) { set.Sync = model.ConfigSyncSettings{} })
		s.resetSeal()
		s.setSyncStatus(func(st *model.ConfigSyncStatus) { *st = model.ConfigSyncStatus{} })
		s.em.emit(TopicSettings)
		return err
	case model.SyncICloud:
		if s.iCloudDir() == "" {
			return errors.New(i18n.T("没有找到 iCloud 云盘文件夹。请在系统设置中登录 iCloud 并打开 iCloud 云盘。"))
		}
	case model.SyncFolder:
		dir = filepath.Clean(dir)
		if dir == "" || !filepath.IsAbs(dir) {
			return errors.New(i18n.T("请选择同步文件夹。"))
		}
		want.Dir = dir
	default:
		return fmt.Errorf("unknown sync mode %q", mode)
	}
	target := s.syncDir(want)
	if err := os.MkdirAll(target, 0o700); err != nil {
		return fmt.Errorf(i18n.T("无法创建同步文件夹：%v"), err)
	}
	s.cs.runMu.Lock()
	defer s.cs.runMu.Unlock()
	path := filepath.Join(target, syncfile.FileName)
	snap := syncfile.Look(target)
	if snap.Placeholder {
		s.askDownload(path)
		return errors.New(i18n.T("iCloud 正在下载同步文件夹中已有的 lanflow.yaml，请稍后再试。"))
	}
	local := s.snapshotLocal()
	localDoc := buildDoc(local, linksKeychain)
	var fileData []byte
	var fileDoc syncfile.Doc
	if snap.Exists {
		fileData, err = os.ReadFile(path)
		if err != nil {
			return fmt.Errorf(i18n.T("读取同步文件失败：%v"), err)
		}
		var derr error
		fileDoc, derr = syncfile.Decode(fileData, defaultSettings())
		same := derr == nil && bytes.Equal(syncfile.Encode(stripPassword(refsOnly(fileDoc))), syncfile.Encode(localDoc))
		switch {
		case same:
			choice = model.SyncUseFile // nothing to merge
		case choice == model.SyncUseFile && derr != nil:
			return fmt.Errorf(i18n.T("同步文件无效，无法使用：%v"), derr)
		case choice == model.SyncUseFile:
			plain, webdav := s.unsealDoc(fileDoc)
			if _, err := s.applyDoc(plain, false); err != nil {
				return fmt.Errorf(i18n.T("同步文件中的配置未能加载：%v"), err)
			}
			s.adoptWebDAVPassword(webdav)
		case choice == model.SyncUseLocal:
			if err := s.backupSyncFile(path, fileData); err != nil {
				return err
			}
		default:
			return ErrConfigSyncChoice
		}
	}
	if err := s.updateSettings(func(set *model.Settings) { set.Sync = want }); err != nil {
		return err
	}
	if snap.Exists && choice == model.SyncUseFile {
		s.primeSeal(fileDoc) // the file's encryption (and this machine's key for it)
	} else {
		s.resetSeal() // this machine's configuration wins: written with keychain references
		s.markSealedPrimed()
	}
	s.cs.mu.Lock()
	s.cs.snap, s.cs.snapDir, s.cs.changedAt = snap, target, time.Time{}
	s.cs.dirty = true
	s.cs.notified = ""
	if snap.Exists && choice == model.SyncUseFile {
		s.cs.canon = syncfile.Encode(fileDoc)
	} else {
		s.cs.canon = fileData // backed up (or none): write this machine's configuration now
	}
	s.cs.mu.Unlock()
	if snap.Exists && choice == model.SyncUseFile {
		s.rememberHash(path, syncfile.Hash(fileData))
		m := s.missingSecrets(fileDoc)
		s.setSyncStatus(func(st *model.ConfigSyncStatus) {
			*st = model.ConfigSyncStatus{State: model.SyncStateOK, LastAppliedAt: s.d.now(), MissingSecrets: m.subs, WebDAVPasswordMissing: m.webdav}
		})
	} else {
		s.setSyncStatus(func(st *model.ConfigSyncStatus) { *st = model.ConfigSyncStatus{State: model.SyncStateOK} })
	}
	s.writeSyncFile(target)
	s.em.emit(TopicSettings)
	return nil
}

func stripPassword(d syncfile.Doc) syncfile.Doc { d.WebDAVPassword = ""; return d }

// refsOnly is d with sealed links written as keychain references, to
// compare a sealed file with this machine's configuration.
func refsOnly(d syncfile.Doc) syncfile.Doc {
	d.Encryption = nil
	d.Subscriptions = append([]syncfile.Subscription(nil), d.Subscriptions...)
	for i, sub := range d.Subscriptions {
		if syncfile.IsSealed(sub.URL) {
			d.Subscriptions[i].URL = syncfile.SubscriptionRef(sub.ID)
		}
	}
	return d
}

// backupSyncFile keeps a copy of a file about to be replaced by the
// user's explicit choice in <DataDir>/sync-backups.
func (s *Service) backupSyncFile(name string, data []byte) error {
	dst := filepath.Join(s.opt.DataDir, syncBackupDir, s.d.now().Format("20060102-150405.000")+"-"+filepath.Base(name))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf(i18n.T("备份原有同步文件失败：%v"), err)
	}
	if err := writeFileAtomic(dst, data, 0o600); err != nil {
		return fmt.Errorf(i18n.T("备份原有同步文件失败：%v"), err)
	}
	return nil
}

// ResolveConfigConflict implements API.ResolveConfigConflict: the user
// keeps the conflict copy name (keepCopy: it is loaded and becomes
// lanflow.yaml) or the current file. The copy not kept is moved to
// <DataDir>/sync-backups, not deleted.
func (s *Service) ResolveConfigConflict(name string, keepCopy bool) (err error) {
	defer func() { s.logOp("resolve config conflict "+name, err) }()
	dir := s.syncDir(s.Settings().Sync)
	if dir == "" {
		return errors.New(i18n.T("配置同步未开启。"))
	}
	if !syncfile.IsConflictCopy(name) || filepath.Base(name) != name {
		return errors.New(i18n.T("不是同步文件的冲突副本。"))
	}
	s.cs.runMu.Lock()
	defer s.cs.runMu.Unlock()
	copyPath := filepath.Join(dir, name)
	data, err := os.ReadFile(copyPath)
	if err != nil {
		return fmt.Errorf(i18n.T("读取冲突副本失败：%v"), err)
	}
	path := filepath.Join(dir, syncfile.FileName)
	if keepCopy {
		doc, err := syncfile.Decode(data, defaultSettings())
		if err != nil {
			return fmt.Errorf(i18n.T("冲突副本无效：%v"), err)
		}
		plain, webdav := s.unsealDoc(doc)
		if _, err := s.applyDoc(plain, false); err != nil {
			return fmt.Errorf(i18n.T("冲突副本中的配置未能加载：%v"), err)
		}
		s.primeSeal(doc)
		s.adoptWebDAVPassword(webdav)
		cur, err := os.ReadFile(path)
		if err == nil {
			if err := s.backupSyncFile(path, cur); err != nil {
				return err
			}
		}
		s.cs.mu.Lock()
		s.cs.canon = cur // backed up: rewrite lanflow.yaml from what was loaded
		s.cs.mu.Unlock()
	}
	if err := s.backupSyncFile(copyPath, data); err != nil {
		return err
	}
	if err := os.Remove(copyPath); err != nil {
		return fmt.Errorf(i18n.T("移走冲突副本失败：%v"), err)
	}
	s.writeSyncFile(dir)
	s.noteConflicts(syncfile.Look(dir).Conflicts)
	return nil
}
