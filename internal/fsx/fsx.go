// Package fsx holds the file-system helpers shared by the stores: an atomic
// file write that survives a crash at any point (the old content or the new
// one, never a mix or an empty file).
package fsx

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// WriteFile atomically replaces path with data: the data goes to a uniquely
// named temp file in the same directory, which is synced, given perm and
// renamed over path; the directory is synced afterwards so the rename
// itself is durable. Concurrent writers never share a temp file: the last
// rename wins and every reader sees one complete version.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	return Write(path, perm, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// Write is WriteFile with the content produced by fill, which writes into
// the temp file (stream large content without building it in memory).
func Write(path string, perm os.FileMode, fill func(io.Writer) error) error {
	tmp, err := CreateTemp(path, perm)
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := fill(tmp); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	return Commit(tmp, path)
}

// CreateTemp creates an empty temp file next to path (making the directory
// when missing) with mode perm. Hand it to Commit to move it into place, or
// close and remove it to give up.
func CreateTemp(path string, perm os.FileMode) (*os.File, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(perm); err != nil && runtime.GOOS != "windows" {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return f, nil
}

// Commit syncs and closes f (from CreateTemp) and renames it over path. On
// any error the temp file is removed and path is left as it was.
func Commit(f *os.File, path string) error {
	name := f.Name()
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(name)
		return fmt.Errorf("sync %s: %w", filepath.Base(path), err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("close %s: %w", filepath.Base(path), err)
	}
	if err := Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// Rename renames oldpath over newpath (replacing it) and syncs the
// directory of newpath so the rename survives a crash.
func Rename(oldpath, newpath string) error {
	if err := os.Rename(oldpath, newpath); err != nil {
		return err
	}
	SyncDir(filepath.Dir(newpath))
	return nil
}

// SyncDir flushes a directory's entries to disk. Best effort: not every
// platform supports it (Windows cannot open a directory for sync), and the
// data itself is already synced.
func SyncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	d.Close()
}
