package app

import (
	"os"
	"path/filepath"
	"runtime"
)

// Options tell NewService where to keep its files.
type Options struct {
	DataDir     string // settings, profiles, core work dir
	LogDir      string // core log
	CacheDir    string // exit probe cache
	ResourceDir string // bundled geodata (geo/) and helper binary
	Mac         bool   // running on macOS (labels and helper binary name)
	// Language is the language of a first run, "zh" or "en" (docs/design.md
	// §5.8), when no settings file exists yet; "" keeps the default (zh).
	// main derives it from the system's preferred languages.
	Language string
}

// DefaultOptions returns the platform's conventional directories for appName:
// macOS ~/Library/Application Support/<app>, ~/Library/Logs/<app>,
// ~/Library/Caches/<app>; Windows %APPDATA%\<app>, %LOCALAPPDATA%\<app>\Logs,
// %LOCALAPPDATA%\<app>\Cache; elsewhere the XDG directories.
func DefaultOptions(appName string) Options {
	home, _ := os.UserHomeDir()
	o := Options{Mac: runtime.GOOS == "darwin", ResourceDir: resourceDir()}
	switch runtime.GOOS {
	case "darwin":
		o.DataDir = filepath.Join(home, "Library", "Application Support", appName)
		o.LogDir = filepath.Join(home, "Library", "Logs", appName)
		o.CacheDir = filepath.Join(home, "Library", "Caches", appName)
	case "windows":
		roaming := os.Getenv("APPDATA")
		if roaming == "" {
			roaming = filepath.Join(home, "AppData", "Roaming")
		}
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		o.DataDir = filepath.Join(roaming, appName)
		o.LogDir = filepath.Join(local, appName, "Logs")
		o.CacheDir = filepath.Join(local, appName, "Cache")
	default:
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(home, ".local", "share")
		}
		state := os.Getenv("XDG_STATE_HOME")
		if state == "" {
			state = filepath.Join(home, ".local", "state")
		}
		cache := os.Getenv("XDG_CACHE_HOME")
		if cache == "" {
			cache = filepath.Join(home, ".cache")
		}
		o.DataDir = filepath.Join(data, appName)
		o.LogDir = filepath.Join(state, appName, "logs")
		o.CacheDir = filepath.Join(cache, appName)
	}
	return o
}

// resourceDir finds the directory holding bundled resources: the app
// bundle's Resources folder on macOS, a resources/ folder or the executable's
// own folder otherwise, and ./bin during development.
func resourceDir() string {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, "..", "Resources"),
			filepath.Join(dir, "resources"),
			dir,
		)
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "bin"), filepath.Join(wd, "resources"))
	}
	for _, c := range candidates {
		if st, err := os.Stat(filepath.Join(c, "geo")); err == nil && st.IsDir() {
			return filepath.Clean(c)
		}
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return filepath.Clean(c)
		}
	}
	return "."
}

// helperBinary returns the path of the bundled lanflow-helper.
func (o Options) helperBinary() string {
	name := "lanflow-helper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(o.ResourceDir, name)
}
