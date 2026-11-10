package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Hooks for tests; production code uses the os defaults.
var (
	executablePath = os.Executable
	getwd          = os.Getwd
)

// notFoundHint is appended to ErrNotFound so the user knows what to do.
const notFoundHint = "：请运行 scripts/fetch-core.sh 下载内核，或在「设置 → 内核」中指定 mihomo 可执行文件的路径"

// findBinary implements FindBinary.
func findBinary(custom string) (string, error) {
	if custom = strings.TrimSpace(custom); custom != "" {
		abs, err := filepath.Abs(custom)
		if err != nil {
			return "", fmt.Errorf("内核路径无效 %q：%w", custom, err)
		}
		if err := checkBinary(abs); err != nil {
			return "", fmt.Errorf("自定义内核路径不可用：%w", err)
		}
		return abs, nil
	}
	for _, c := range bundledCandidates() {
		if checkBinary(c) == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("%w%s", ErrNotFound, notFoundHint)
}

// bundledCandidates lists where a shipped core may live, in priority order:
// macOS app bundle Resources, next to the executable, ./bin during development.
func bundledCandidates() []string {
	var out []string
	if exe, err := executablePath(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		dir := filepath.Dir(exe)
		out = append(out,
			filepath.Join(dir, "..", "Resources", "mihomo"),
			filepath.Join(dir, binaryName),
		)
	}
	if cwd, err := getwd(); err == nil {
		out = append(out, filepath.Join(cwd, "bin", binaryName))
	}
	for i := range out {
		out[i] = filepath.Clean(out[i])
	}
	return out
}

// checkBinary verifies path is a regular, executable file.
func checkBinary(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s 不存在", path)
		}
		return fmt.Errorf("无法访问 %s：%w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s 不是普通文件", path)
	}
	if !isExecutable(info.Mode()) {
		return fmt.Errorf("%s 没有可执行权限", path)
	}
	return nil
}

// cmdTimeout bounds the short-lived helper invocations (-v / -t).
const cmdTimeout = 30 * time.Second

func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, cmdTimeout)
}

// versionRe matches the first line of `mihomo -v`:
// "Mihomo Meta v1.19.33 darwin arm64 with go1.26.10 ...".
var versionRe = regexp.MustCompile(`(?m)^Mihomo(?: Meta)?\s+(\S+)`)

// parseVersion extracts the version token from `mihomo -v` output.
func parseVersion(out string) (string, error) {
	m := versionRe.FindStringSubmatch(out)
	if m == nil {
		first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
		return "", fmt.Errorf("无法识别内核版本输出：%q", strings.TrimSpace(first))
	}
	v := m[1]
	if len(v) > 0 && v[0] >= '0' && v[0] <= '9' {
		v = "v" + v
	}
	return v, nil
}

// version implements Version.
func version(ctx context.Context, bin string) (string, error) {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-v")
	cmd.SysProcAttr = sysProcAttr()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("无法获取内核版本（%s）：%w", bin, describeExecErr(err, out))
	}
	return parseVersion(string(out))
}

// logMsgRe pulls msg="..." out of a logrus text line.
var logMsgRe = regexp.MustCompile(`msg=("(?:[^"\\]|\\.)*"|\S+)`)

// shapeValidateError turns the combined -t output into a concise message,
// keeping the core's decisive line (its level=error log entry).
func shapeValidateError(out string) string {
	var errLines, other []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case strings.Contains(line, "level=error"), strings.Contains(line, "level=fatal"):
			errLines = append(errLines, logMessage(line))
		case strings.Contains(line, "level="):
			// info/warning noise
		case strings.HasPrefix(line, "configuration file ") && strings.HasSuffix(line, "test failed"),
			line == "configuration test failed":
			// the generic trailer; the real reason is in errLines
		default:
			other = append(other, line)
		}
	}
	reason := strings.Join(errLines, "；")
	if reason == "" {
		reason = strings.Join(other, "；")
	}
	if reason == "" {
		reason = "内核未给出具体原因"
	}
	return "配置校验失败：" + reason
}

// logMessage returns the msg field of a logrus line, or the line itself.
func logMessage(line string) string {
	m := logMsgRe.FindStringSubmatch(line)
	if m == nil {
		return line
	}
	s := m[1]
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

// validate implements Validate.
func validate(ctx context.Context, bin, workDir, config string) error {
	ctx, cancel := withTimeout(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-t", "-d", workDir, "-f", config)
	cmd.Dir = workDir
	cmd.SysProcAttr = sysProcAttr()
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ctx.Err() == nil {
		return errors.New(shapeValidateError(string(out)))
	}
	return fmt.Errorf("无法运行内核进行配置校验（%s）：%w", bin, describeExecErr(err, out))
}

// describeExecErr adds stderr context to a failed command error when the
// error itself carries none.
func describeExecErr(err error, out []byte) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		stderr := bytes.TrimSpace(ee.Stderr)
		if len(stderr) == 0 {
			stderr = bytes.TrimSpace(out)
		}
		if len(stderr) > 0 {
			return fmt.Errorf("%w：%s", err, lastLine(string(stderr)))
		}
	}
	return err
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
