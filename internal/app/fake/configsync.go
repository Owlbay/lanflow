package fake

import (
	"errors"
	"path/filepath"

	"lanflow/internal/app"
	"lanflow/internal/model"
	"lanflow/internal/syncfile"
)

// Configuration sync: the fake keeps the setting and a status; the
// folder is never touched.

// SyncTest lets view tests shape the sync answers.
type SyncTest struct {
	NeedChoice bool                    // SetConfigSync without a choice returns app.ErrConfigSyncChoice
	Choices    []string                // the choices SetConfigSync received
	Status     *model.ConfigSyncStatus // returned by ConfigSyncStatus when set
	Resolved   []string                // ResolveConfigConflict calls, "name:copy" or "name:file"
	Passphrase string                  // the right passphrase for UnlockSync ("" = any)
	Calls      []string                // SetSyncPassphrase / UnlockSync / ForgetSyncPassphrase calls ("set", "off", "unlock", "forget")
}

// Sync is the fake's SyncTest (guarded by the fake's lock: set it before use).
func (f *Fake) Sync() *SyncTest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.syncTest == nil {
		f.syncTest = &SyncTest{}
	}
	return f.syncTest
}

func (f *Fake) ConfigSyncStatus() model.ConfigSyncStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.syncTest != nil && f.syncTest.Status != nil {
		return *f.syncTest.Status
	}
	st := model.ConfigSyncStatus{State: model.SyncStateOff, Mode: f.set.Sync.Mode}
	switch f.set.Sync.Mode {
	case model.SyncICloud:
		st.Dir = "/Users/demo/Library/Mobile Documents/com~apple~CloudDocs/Lanflow"
	case model.SyncFolder:
		st.Dir = f.set.Sync.Dir
	}
	if st.Dir != "" {
		st.Path, st.State = filepath.Join(st.Dir, "lanflow.yaml"), model.SyncStateOK
	}
	return st
}

func (f *Fake) SetConfigSync(mode, dir, choice string) error {
	return f.do(func() error {
		if f.syncTest != nil {
			f.syncTest.Choices = append(f.syncTest.Choices, choice)
			if f.syncTest.NeedChoice && choice == "" && mode != model.SyncOff {
				return app.ErrConfigSyncChoice
			}
		}
		switch mode {
		case model.SyncOff, model.SyncICloud:
			dir = ""
		case model.SyncFolder:
			if dir == "" {
				return errors.New("请选择同步文件夹。")
			}
		}
		f.set.Sync = model.ConfigSyncSettings{Mode: mode, Dir: dir}
		return nil
	}, app.TopicSettings)
}

func (f *Fake) ResolveConfigConflict(name string, keepCopy bool) error {
	return f.do(func() error {
		if f.syncTest == nil {
			return errors.New("配置同步未开启。")
		}
		which := "file"
		if keepCopy {
			which = "copy"
		}
		f.syncTest.Resolved = append(f.syncTest.Resolved, name+":"+which)
		return nil
	}, app.TopicSettings)
}

func (f *Fake) ApplyConfigFile(path string, dryRun bool) (model.ConfigApplyResult, error) {
	if path == "" {
		return model.ConfigApplyResult{}, errors.New("配置同步未开启，请指定要加载的文件。")
	}
	return model.ConfigApplyResult{Summary: "文件与本机配置一致，没有需要加载的内容。"}, nil
}

func (f *Fake) SetSyncPassphrase(passphrase string) error {
	return f.do(func() error {
		if f.syncTest == nil {
			f.syncTest = &SyncTest{}
		}
		if passphrase == "" {
			f.syncTest.Calls = append(f.syncTest.Calls, "off")
			if f.syncTest.Status != nil {
				f.syncTest.Status.Encrypted, f.syncTest.Status.Locked = false, false
			}
			return nil
		}
		if len([]rune(passphrase)) < syncfile.MinPassphraseLen {
			return syncfile.ErrShortPassphrase
		}
		f.syncTest.Calls = append(f.syncTest.Calls, "set")
		if f.syncTest.Status != nil {
			f.syncTest.Status.Encrypted, f.syncTest.Status.Locked = true, false
		}
		return nil
	}, app.TopicSettings)
}

func (f *Fake) UnlockSync(passphrase string) error {
	return f.do(func() error {
		if f.syncTest == nil {
			f.syncTest = &SyncTest{}
		}
		f.syncTest.Calls = append(f.syncTest.Calls, "unlock")
		if f.syncTest.Passphrase != "" && passphrase != f.syncTest.Passphrase {
			return syncfile.ErrWrongPassphrase
		}
		if f.syncTest.Status != nil {
			f.syncTest.Status.Locked, f.syncTest.Status.PassphraseChanged, f.syncTest.Status.LockedSecrets = false, false, 0
		}
		return nil
	}, app.TopicSettings)
}

func (f *Fake) ForgetSyncPassphrase() error {
	return f.do(func() error {
		if f.syncTest == nil {
			f.syncTest = &SyncTest{}
		}
		f.syncTest.Calls = append(f.syncTest.Calls, "forget")
		if f.syncTest.Status != nil && f.syncTest.Status.Encrypted {
			f.syncTest.Status.Locked = true
		}
		return nil
	}, app.TopicSettings)
}
