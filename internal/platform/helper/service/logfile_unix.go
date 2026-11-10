//go:build !windows

package service

import (
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// openLogFile rotates path at LogRotateSize (keeping path.1), opens it for
// appending and hands it (and path.1) to the user.
//
// path lies in the user's data directory, so root never follows a symlink
// there (in any component) and only takes over a regular file with a
// single link owned by root or the user; ownership is changed with fchown
// on the opened descriptor, never by path.
func openLogFile(path string, uid string) (io.WriteCloser, error) {
	n, err := parseUID(uid)
	if err != nil {
		return nil, err
	}
	dir, base := filepath.Dir(path), filepath.Base(path)
	dirfd, err := openDirNoFollow(dir)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirfd)

	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, base, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil &&
		fileMode(&st) == unix.S_IFREG && st.Size >= LogRotateSize {
		// renameat moves the directory entry itself; it never follows a
		// symlink, and the old file is checked below before any chown.
		_ = unix.Renameat(dirfd, base, dirfd, base+".1")
	}

	fd, err := openAtChecked(dirfd, base, unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT, 0o600, n, true)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	if err := handOverFd(fd, n); err != nil {
		unix.Close(fd)
		return nil, &os.PathError{Op: "fchown", Path: path, Err: err}
	}
	if old, err := openAtChecked(dirfd, base+".1", unix.O_RDONLY, 0, n, true); err == nil {
		_ = handOverFd(old, n)
		unix.Close(old)
	}
	return os.NewFile(uintptr(fd), path), nil
}
