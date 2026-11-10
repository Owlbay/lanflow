//go:build !windows

package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func samePath(a, b string) bool           { return a == b }
func hasPathPrefix(p, prefix string) bool { return strings.HasPrefix(p, prefix) }

// ownerMatches reports whether path is owned by uid (decimal).
func ownerMatches(path string, uid string) (bool, error) {
	want, err := strconv.ParseUint(uid, 10, 32)
	if err != nil {
		return false, fmt.Errorf("用户标识无效: %q", uid)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false, errors.New("无法读取文件属主")
	}
	return uint64(st.Uid) == want, nil
}

// checkController validates the controller unix socket path: absolute,
// its parent directory must exist and be owned by the installing user
// (Mihomo removes whatever is at the path before listening, so a root
// Mihomo must never be pointed at a directory another user controls), and
// the path itself must be absent or an existing socket.
func checkController(p *Policy, controller string) error {
	if controller == "" {
		return errors.New("控制器地址未指定")
	}
	if !filepath.IsAbs(controller) {
		return fmt.Errorf("控制器地址必须是绝对路径: %s", controller)
	}
	controller = filepath.Clean(controller)
	dir, err := filepath.EvalSymlinks(filepath.Dir(controller))
	if err != nil {
		return fmt.Errorf("控制器所在目录不可用: %w", err)
	}
	if p.OwnerMatches != nil {
		ok, err := p.OwnerMatches(dir, p.Config.UID)
		if err != nil {
			return fmt.Errorf("无法检查控制器目录属主: %w", err)
		}
		if !ok {
			return fmt.Errorf("控制器所在目录不属于当前用户: %s", dir)
		}
	}
	if fi, err := os.Lstat(filepath.Join(dir, filepath.Base(controller))); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("控制器路径已被占用且不是套接字: %s", controller)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
