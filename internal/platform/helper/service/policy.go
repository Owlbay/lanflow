package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"lanflow/internal/platform/helper/protocol"
)

// Policy decides which start requests the helper accepts. It is derived
// from the recorded Config and never from the client.
type Policy struct {
	Config Config
	// OwnerMatches reports whether path (a directory, symlinks already
	// resolved) is owned by the installing user. Used for the controller
	// socket's parent directory on macOS. Nil skips the check.
	OwnerMatches func(path string, uid string) (bool, error)
	// CheckController validates the controller endpoint and returns the
	// form the helper uses (on unix: its parent directory resolved, so the
	// socket hand-over later never re-resolves user-controlled symlinks).
	// Nil means the platform default (set by policy_<os>.go).
	CheckController func(p *Policy, controller string) (string, error)
}

// NewPolicy returns a policy for c with platform defaults.
func NewPolicy(c Config) *Policy {
	return &Policy{Config: c, OwnerMatches: ownerMatches, CheckController: checkController}
}

// Validate checks spec and returns the spec the helper will actually run:
// Bin replaced by the privileged copy and paths cleaned.
func (p *Policy) Validate(spec protocol.Spec) (protocol.Spec, error) {
	out := spec
	c := p.Config

	// Binary: only the recorded copy (or the source it was copied from,
	// when that source is still byte-identical) is acceptable.
	switch {
	case spec.Bin == c.Core:
	case c.CoreSource != "" && spec.Bin == c.CoreSource:
		sum, err := FileSHA256(spec.Bin)
		if err != nil {
			return out, fmt.Errorf("无法读取内核 %s: %w", spec.Bin, err)
		}
		if !strings.EqualFold(sum, c.SHA256) {
			return out, errors.New("Mihomo 内核已更新，与权限组件记录的版本不一致，请重新安装权限组件")
		}
	default:
		return out, fmt.Errorf("权限组件拒绝运行未登记的内核: %s", spec.Bin)
	}
	sum, err := FileSHA256(c.Core)
	if err != nil {
		return out, fmt.Errorf("无法读取已登记的内核 %s: %w", c.Core, err)
	}
	if !strings.EqualFold(sum, c.SHA256) {
		return out, errors.New("已登记的 Mihomo 内核校验失败，请重新安装权限组件")
	}
	out.Bin = c.Core

	dataDir, err := filepath.EvalSymlinks(c.DataDir)
	if err != nil {
		return out, fmt.Errorf("数据目录不可用: %w", err)
	}

	out.WorkDir, err = p.insideDataDir(dataDir, spec.WorkDir, "工作目录")
	if err != nil {
		return out, err
	}
	if fi, err := os.Stat(out.WorkDir); err != nil || !fi.IsDir() {
		return out, fmt.Errorf("工作目录不存在或不是目录: %s", spec.WorkDir)
	}
	out.Config, err = p.insideDataDir(dataDir, spec.Config, "配置文件")
	if err != nil {
		return out, err
	}
	if fi, err := os.Stat(out.Config); err != nil || !fi.Mode().IsRegular() {
		return out, fmt.Errorf("配置文件不存在或不是普通文件: %s", spec.Config)
	}
	out.LogFile, err = p.insideDataDir(dataDir, spec.LogFile, "日志文件")
	if err != nil {
		return out, err
	}
	if fi, err := os.Lstat(out.LogFile); err == nil && !fi.Mode().IsRegular() {
		return out, fmt.Errorf("日志文件不是普通文件: %s", spec.LogFile)
	}

	check := p.CheckController
	if check == nil {
		check = checkController
	}
	out.Controller, err = check(p, spec.Controller)
	if err != nil {
		return out, err
	}

	out.Env, err = FilterEnv(spec.Env)
	if err != nil {
		return out, err
	}
	return out, nil
}

// insideDataDir cleans path, resolves symlinks (of the deepest existing
// prefix, so a not-yet-created log file still validates) and requires the
// result to be dataDir or below it.
func (p *Policy) insideDataDir(dataDir, path, what string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%s未指定", what)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s必须是绝对路径: %s", what, path)
	}
	resolved, err := resolvePath(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("%s路径无效: %w", what, err)
	}
	if !isWithin(dataDir, resolved) {
		return "", fmt.Errorf("%s必须位于 Lanflow 数据目录内: %s", what, path)
	}
	return resolved, nil
}

// resolvePath is EvalSymlinks that tolerates a missing final components:
// the longest existing prefix is resolved and the rest appended.
func resolvePath(path string) (string, error) {
	var rest []string
	cur := path
	for {
		r, err := filepath.EvalSymlinks(cur)
		if err == nil {
			parts := append([]string{r}, rest...)
			return filepath.Join(parts...), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = parent
	}
}

// isWithin reports whether path equals dir or is below it.
func isWithin(dir, path string) bool {
	dir = filepath.Clean(dir)
	path = filepath.Clean(path)
	if samePath(dir, path) {
		return true
	}
	if !strings.HasSuffix(dir, string(filepath.Separator)) {
		dir += string(filepath.Separator)
	}
	return hasPathPrefix(path, dir)
}

// Environment policy. Only an exact list of harmless tuning variables is
// passed to the root Mihomo. Everything else is refused, in particular
// SAFE_PATHS / SKIP_SAFE_PATH_CHECK (they widen the paths Mihomo may read
// and write as root), every CLASH_* variable (post-up/down scripts run as
// root, controller/secret/config overrides), DYLD_*, LD_*, PATH, GODEBUG.
var allowedEnv = map[string]bool{
	"DISABLE_LOOPBACK_DETECTOR": true,
	"GOGC":                      true,
	"GOMEMLIMIT":                true,
}

// FilterEnv returns the subset of env the helper passes to Mihomo, or an
// error naming the first refused variable.
func FilterEnv(env []string) ([]string, error) {
	var out []string
	for _, kv := range env {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("环境变量格式错误: %q", kv)
		}
		if !envAllowed(key) {
			return nil, fmt.Errorf("权限组件不允许传递环境变量 %s", key)
		}
		out = append(out, kv)
	}
	return out, nil
}

func envAllowed(key string) bool { return allowedEnv[key] }
