//go:build !windows

package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// The helper runs as root but works on paths inside the user's data
// directory, which the user can rewrite at any moment. Every privileged
// file operation therefore goes through directory file descriptors:
//
//   - openDirNoFollow walks the (already symlink-resolved) path from "/"
//     one component at a time with O_NOFOLLOW, so a symlink planted in any
//     component after validation makes the walk fail instead of leading
//     root somewhere else;
//   - files are opened relative to that fd with O_NOFOLLOW, checked with
//     fstat on the open fd (type, link count, owner) and re-owned with
//     fchown on the same fd, so there is no window between check and use.

// fchown is unix.Fchown; tests replace it to observe which files the
// helper hands over.
var fchown = unix.Fchown

// errUnsafeFile is returned when a path the helper is about to modify is
// not a plain, singly linked file or directory owned by root or the user.
var errUnsafeFile = errors.New("文件类型或属主不安全")

// openDirNoFollow opens dir (absolute, symlinks already resolved) without
// following a symlink in any component.
func openDirNoFollow(dir string) (int, error) {
	if !filepath.IsAbs(dir) {
		return -1, fmt.Errorf("路径必须是绝对路径: %s", dir)
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, name := range strings.Split(filepath.Clean(dir), "/") {
		if name == "" {
			continue
		}
		next, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return -1, &os.PathError{Op: "open", Path: dir, Err: err}
		}
		fd = next
	}
	return fd, nil
}

// fileMode returns the S_IFMT bits of st.
func fileMode(st *unix.Stat_t) uint32 { return uint32(st.Mode) & unix.S_IFMT }

// ownedBy reports whether st belongs to root or uid.
func ownedBy(st *unix.Stat_t, uid int) bool {
	return st.Uid == 0 || int(st.Uid) == uid
}

// checkHandOver accepts a regular file with exactly one link or a
// directory, owned by root or uid. A second link means the name may be a
// hard link to a file elsewhere (for example in /etc) that root must not
// give away.
func checkHandOver(st *unix.Stat_t, uid int) error {
	switch fileMode(st) {
	case unix.S_IFREG:
		if st.Nlink != 1 {
			return fmt.Errorf("%w: 存在 %d 个硬链接", errUnsafeFile, st.Nlink)
		}
	case unix.S_IFDIR:
	default:
		return fmt.Errorf("%w: 不是普通文件或目录", errUnsafeFile)
	}
	if !ownedBy(st, uid) {
		return fmt.Errorf("%w: 属主 %d", errUnsafeFile, st.Uid)
	}
	return nil
}

// openAtChecked opens name in dirfd with flags|O_NOFOLLOW|O_NONBLOCK (a
// FIFO must not block root), verifies the opened file with checkHandOver
// and, when wantRegular, that it is a regular file. The returned fd is in
// blocking mode.
func openAtChecked(dirfd int, name string, flags int, perm uint32, uid int, wantRegular bool) (int, error) {
	fd, err := unix.Openat(dirfd, name, flags|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, perm)
	if err != nil {
		return -1, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return -1, err
	}
	if err := checkHandOver(&st, uid); err != nil {
		unix.Close(fd)
		return -1, err
	}
	if wantRegular && fileMode(&st) != unix.S_IFREG {
		unix.Close(fd)
		return -1, fmt.Errorf("%w: 不是普通文件", errUnsafeFile)
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// handOverFd gives the open file to uid (group unchanged).
func handOverFd(fd int, uid int) error { return fchown(fd, uid, -1) }
