package main

import (
	"log"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/egoist/mygo"

	"lanflow/internal/autostart"
)

var migrateLogin sync.Once

// applyAutostart makes the login start the service process
// (internal/autostart: a launch agent on macOS, a Run entry on Windows)
// when Settings.OpenAtLogin is on. The login item of the versions before
// the split (MyGo's, which opens the app, i.e. the window) is removed the
// first time: the service started at login opens the window itself
// unless Settings.SilentStart (docs/service-split.md S3).
func applyAutostart(exe string, open bool) {
	migrateLogin.Do(func() {
		if mygo.App.OpenAtLogin() {
			if err := mygo.App.SetOpenAtLogin(false); err != nil {
				log.Printf("lanflow: remove the old login item: %v", err)
			} else {
				log.Printf("lanflow: login item moved to the service process")
			}
		}
	})
	if runtime.GOOS == "darwin" && !mygo.App.IsPackaged() {
		return // development builds: no stable path to start at login
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	changed, err := autostart.Apply(exe, open)
	switch {
	case err != nil:
		log.Printf("lanflow: open at login: %v", err)
	case changed:
		log.Printf("lanflow: open at login: %v (%s)", open, exe)
	}
}
