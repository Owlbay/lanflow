package fake

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
	"lanflow/internal/model"
)

// The fake's side of the second feature batch: the WebDAV backup keeps
// an in-memory list of remote files and a password "in the keychain";
// the mirror test answers canned round trips. The Wi-Fi rules need no
// fake behaviour beyond the settings (NetInfo reports "Home-5G").

type feat2 struct {
	password string
	backups  []model.WebDAVBackup
	busy     bool
	last     time.Time
}

// f2state returns the lazily allocated state; f.mu must be held.
func (f *Fake) f2state() *feat2 {
	if f.f2 == nil {
		f.f2 = &feat2{backups: []model.WebDAVBackup{
			{Name: "lanflow-backup-20261108-213000.json", Size: 18_432, Modified: time.Date(2026, 11, 8, 21, 30, 0, 0, time.Local)},
			{Name: "lanflow-backup-20261101-090000.json", Size: 18_102, Modified: time.Date(2026, 11, 1, 9, 0, 0, 0, time.Local)},
		}}
	}
	return f.f2
}

// TestMirrors answers canned round trips, the last preset failing.
func (f *Fake) TestMirrors() []model.MirrorTest {
	f.wait()
	out := make([]model.MirrorTest, len(app.MirrorPresets))
	for i, m := range app.MirrorPresets {
		out[i] = model.MirrorTest{URL: m, Ms: 180 + 130*i}
	}
	if n := len(out); n > 0 {
		out[n-1] = model.MirrorTest{URL: out[n-1].URL, Ms: -1, Error: i18n.T("超时")}
	}
	return out
}

func (f *Fake) WebDAVInfo() model.WebDAVInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.f2state()
	return model.WebDAVInfo{PasswordStore: model.PasswordStoreKeychain, HasPassword: st.password != "", Busy: st.busy, LastBackupAt: st.last}
}

func (f *Fake) SetWebDAVPassword(pw string) error {
	return f.do(func() error { f.f2state().password = pw; return nil }, app.TopicSettings)
}

// webdavCheck is what every remote call needs.
func (f *Fake) webdavCheck() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := strings.TrimSpace(f.set.WebDAV.URL)
	switch {
	case u == "":
		return errors.New(i18n.T("请先填写 WebDAV 服务器地址。"))
	case !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://"):
		return errors.New(i18n.T("WebDAV 地址须是以 http:// 或 https:// 开头的完整地址"))
	case strings.Contains(u, "bad"):
		return errors.New(i18n.T("WebDAV 登录被拒绝，请检查用户名和密码"))
	}
	return nil
}

func (f *Fake) setBusy(on bool) {
	f.mu.Lock()
	f.f2state().busy = on
	f.mu.Unlock()
	f.emit(app.TopicSettings)
}

func (f *Fake) TestWebDAV() error {
	f.setBusy(true)
	defer f.setBusy(false)
	f.wait()
	return f.webdavCheck()
}

func (f *Fake) BackupNow() error {
	f.setBusy(true)
	defer f.setBusy(false)
	f.wait()
	if err := f.webdavCheck(); err != nil {
		return err
	}
	f.mu.Lock()
	st := f.f2state()
	now := time.Now()
	st.backups = append([]model.WebDAVBackup{{Name: "lanflow-backup-" + now.Format("20060102-150405") + ".json", Size: 18_600, Modified: now}}, st.backups...)
	if keep := f.set.WebDAV.Keep; keep > 0 && len(st.backups) > keep {
		st.backups = st.backups[:keep]
	}
	st.last = now
	f.set.WebDAV.LastBackupAt = now
	f.mu.Unlock()
	f.emit(app.TopicSettings)
	return nil
}

func (f *Fake) ListBackups() ([]model.WebDAVBackup, error) {
	f.wait()
	if err := f.webdavCheck(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.WebDAVBackup(nil), f.f2state().backups...), nil
}

func (f *Fake) RestoreBackup(name string) (string, error) {
	f.setBusy(true)
	defer f.setBusy(false)
	f.wait()
	if err := f.webdavCheck(); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.f2state().backups {
		if b.Name == name {
			return fmt.Sprintf(i18n.T("已从 %s 恢复设置、覆写与 2 个订阅"), name), nil
		}
	}
	return "", errors.New(i18n.T("WebDAV 路径不存在（HTTP 404）"))
}

func (f *Fake) DeleteBackup(name string) error {
	return f.do(func() error {
		st := f.f2state()
		for i, b := range st.backups {
			if b.Name == name {
				st.backups = append(st.backups[:i], st.backups[i+1:]...)
				return nil
			}
		}
		return errors.New(i18n.T("WebDAV 路径不存在（HTTP 404）"))
	}, app.TopicSettings)
}
