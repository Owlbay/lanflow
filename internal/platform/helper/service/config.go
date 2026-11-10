// Package service implements the privileged side of lanflow-helper: the
// recorded install configuration, request validation, the Mihomo process
// supervisor, the protocol handler, and the platform-specific install /
// listen / run code used by cmd/lanflow-helper.
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ConfigVersion is the on-disk format version of Config.
const ConfigVersion = 1

// Config is written by `lanflow-helper install` into a root/SYSTEM-owned
// file and read by `lanflow-helper run`. Everything the helper trusts comes
// from here, never from the client.
type Config struct {
	Version int `json:"version"`
	// Core is the privileged copy of the Mihomo binary the helper executes.
	Core string `json:"core"`
	// CoreSource is the path the installer copied Core from (the binary
	// bundled with the app). Clients may name either path in Spec.Bin.
	CoreSource string `json:"coreSource"`
	// SHA256 is the hex digest of Core at install time.
	SHA256 string `json:"sha256"`
	// DataDir is the installing user's Lanflow data directory (symlinks
	// resolved). Work dir, config and log file must live inside it.
	DataDir string `json:"dataDir"`
	// UID identifies the installing user: a decimal uid on macOS, a SID
	// string on Windows.
	UID string `json:"uid"`
}

// Validate checks that the config is complete.
func (c Config) Validate() error {
	switch {
	case c.Version != ConfigVersion:
		return fmt.Errorf("配置版本不支持: %d", c.Version)
	case c.Core == "" || !filepath.IsAbs(c.Core):
		return errors.New("配置缺少内核路径")
	case len(c.SHA256) != sha256.Size*2:
		return errors.New("配置缺少内核校验值")
	case c.DataDir == "" || !filepath.IsAbs(c.DataDir):
		return errors.New("配置缺少数据目录")
	case c.UID == "":
		return errors.New("配置缺少用户标识")
	}
	return nil
}

// LoadConfig reads and validates a config file.
func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("解析 %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// SaveConfig writes the config with the given mode (0644 for root-owned
// files: world-readable, admin-writable).
func SaveConfig(path string, c Config, mode os.FileMode) error {
	if err := c.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// FileSHA256 returns the hex SHA-256 of a file.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
