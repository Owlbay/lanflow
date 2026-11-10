//go:build !windows

package service

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

// resolvedTempDir is t.TempDir with symlinks resolved (/var -> /private/var
// on macOS), as the policy hands the helper resolved paths.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// recordFchown replaces fchown with a recorder of the inode numbers it is
// called on (and still performs the chown, which is a no-op to our uid).
func recordFchown(t *testing.T) func() []uint64 {
	t.Helper()
	var mu sync.Mutex
	var inos []uint64
	old := fchown
	fchown = func(fd, uid, gid int) error {
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err == nil {
			mu.Lock()
			inos = append(inos, uint64(st.Ino))
			mu.Unlock()
		}
		return old(fd, uid, gid)
	}
	t.Cleanup(func() { fchown = old })
	return func() []uint64 {
		mu.Lock()
		defer mu.Unlock()
		return append([]uint64(nil), inos...)
	}
}

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		t.Fatal(err)
	}
	return uint64(st.Ino)
}

func myUID() string { return strconv.Itoa(os.Getuid()) }

func TestOpenDirNoFollowRejectsSymlinkComponent(t *testing.T) {
	d := resolvedTempDir(t)
	real := filepath.Join(d, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(d, "link")); err != nil {
		t.Fatal(err)
	}
	fd, err := openDirNoFollow(real)
	if err != nil {
		t.Fatal(err)
	}
	unix.Close(fd)
	if fd, err := openDirNoFollow(filepath.Join(d, "link")); err == nil {
		unix.Close(fd)
		t.Fatal("followed a symlinked final component")
	}
	if err := os.Mkdir(filepath.Join(real, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if fd, err := openDirNoFollow(filepath.Join(d, "link", "sub")); err == nil {
		unix.Close(fd)
		t.Fatal("followed a symlinked intermediate component")
	}
}

func TestOpenLogFileRefusesSymlink(t *testing.T) {
	d := resolvedTempDir(t)
	target := filepath.Join(d, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(d, "core.log")
	if err := os.Symlink(target, logPath); err != nil {
		t.Fatal(err)
	}
	inos := recordFchown(t)
	if w, err := openLogFile(logPath, myUID()); err == nil {
		_ = w.Close()
		t.Fatal("opened a symlinked log file")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" || len(inos()) != 0 {
		t.Fatalf("target touched: %q, fchown on %v", b, inos())
	}
}

func TestOpenLogFileRefusesSymlinkedDirectory(t *testing.T) {
	d := resolvedTempDir(t)
	elsewhere := filepath.Join(d, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(elsewhere, "core.log"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// The validated path was d/logs/core.log; logs is swapped for a
	// symlink afterwards.
	if err := os.Symlink(elsewhere, filepath.Join(d, "logs")); err != nil {
		t.Fatal(err)
	}
	inos := recordFchown(t)
	if w, err := openLogFile(filepath.Join(d, "logs", "core.log"), myUID()); err == nil {
		_ = w.Close()
		t.Fatal("followed a symlinked directory")
	}
	if len(inos()) != 0 {
		t.Fatalf("fchown on %v", inos())
	}
}

func TestOpenLogFileRefusesHardLink(t *testing.T) {
	d := resolvedTempDir(t)
	target := filepath.Join(d, "sensitive")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(d, "core.log")
	if err := os.Link(target, logPath); err != nil {
		t.Fatal(err)
	}
	inos := recordFchown(t)
	if w, err := openLogFile(logPath, myUID()); err == nil {
		_ = w.Close()
		t.Fatal("opened a hard-linked log file")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" || len(inos()) != 0 {
		t.Fatalf("target touched: %q, fchown on %v", b, inos())
	}
}

func TestOpenLogFileRefusesFIFO(t *testing.T) {
	d := resolvedTempDir(t)
	logPath := filepath.Join(d, "core.log")
	if err := unix.Mkfifo(logPath, 0o600); err != nil {
		t.Skip("mkfifo:", err)
	}
	// Must not block waiting for a reader, and must refuse.
	if w, err := openLogFile(logPath, myUID()); err == nil {
		_ = w.Close()
		t.Fatal("opened a FIFO")
	}
}

func TestOpenLogFileRotatedHardLinkNotHandedOver(t *testing.T) {
	d := resolvedTempDir(t)
	logPath := filepath.Join(d, "core.log")
	target := filepath.Join(d, "sensitive")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, logPath+".1"); err != nil {
		t.Fatal(err)
	}
	inos := recordFchown(t)
	w, err := openLogFile(logPath, myUID())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(w, "x")
	_ = w.Close()
	got := inos()
	if len(got) != 1 || got[0] != inode(t, logPath) {
		t.Fatalf("fchown on %v, want only the new log %d", got, inode(t, logPath))
	}
}
