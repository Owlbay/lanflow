package service

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"lanflow/internal/platform/helper/protocol"
)

// InstallOptions are the arguments of `lanflow-helper install`, which runs
// elevated (root / Administrator).
type InstallOptions struct {
	// CoreBin is the Mihomo binary to copy into the privileged location.
	CoreBin string
	// DataDir is the installing user's Lanflow data directory.
	DataDir string
	// UID identifies the installing user: decimal uid (macOS) or SID (Windows).
	UID string
	// Version is the version of the installing helper binary.
	Version string
	// AllowDowngrade permits replacing a newer installed helper.
	AllowDowngrade bool
}

// Logf is the logging callback used by the install and run entry points.
type Logf func(format string, args ...any)

func (o InstallOptions) check() error {
	switch {
	case o.CoreBin == "" || !filepath.IsAbs(o.CoreBin):
		return errors.New("--core 必须是 Mihomo 内核的绝对路径")
	case o.DataDir == "" || !filepath.IsAbs(o.DataDir):
		return errors.New("--data-dir 必须是数据目录的绝对路径")
	case o.UID == "":
		return errors.New("--uid 未指定")
	}
	if fi, err := os.Stat(o.CoreBin); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("内核文件不可用: %s", o.CoreBin)
	}
	return nil
}

// checkDowngrade refuses to replace the helper recorded in configPath when
// it is newer than o.Version (both must be release versions; "dev" or a
// missing record never blocks).
func checkDowngrade(configPath string, o InstallOptions) error {
	if o.AllowDowngrade {
		return nil
	}
	cur, err := LoadConfig(configPath)
	if err != nil {
		return nil
	}
	if c, ok := protocol.CompareVersions(cur.HelperVersion, o.Version); ok && c > 0 {
		return fmt.Errorf("已安装的权限组件 %s 比要安装的 %s 更新，拒绝降级（如确需降级请加 --allow-downgrade）", cur.HelperVersion, o.Version)
	}
	return nil
}

// resolveDataDir cleans and resolves the data dir and requires it to be a
// directory owned by the installing user (unix).
func resolveDataDir(dir, uid string) (string, error) {
	resolved, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err != nil {
		return "", fmt.Errorf("数据目录不可用: %w", err)
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("数据目录不是目录: %s", dir)
	}
	ok, err := ownerMatches(resolved, uid)
	if err != nil {
		return "", fmt.Errorf("无法检查数据目录属主: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("数据目录不属于用户 %s: %s", uid, dir)
	}
	return resolved, nil
}

// copyFile copies src to dst atomically (temp file + rename) with mode.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".lanflow-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmpName) }
	if _, err := io.Copy(tmp, in); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
