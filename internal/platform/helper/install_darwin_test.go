//go:build darwin

package helper

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/plain/path":             "'/plain/path'",
		"/with space/x":           "'/with space/x'",
		"it's":                    `'it'\''s'`,
		`$(rm -rf /) ; echo "hi"`: `'$(rm -rf /) ; echo "hi"'`,
		"back\\slash":             "'back\\slash'",
		"":                        "''",
		"中文 目录":                   "'中文 目录'",
		"a'b'c":                   `'a'\''b'\''c'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

// TestShellCommandRoundTrip feeds the quoted command line through /bin/sh
// and checks every argument arrives intact.
func TestShellCommandRoundTrip(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	args := []string{"install", "--core", "/Applications/澜流 Lanflow.app/Contents/Resources/mihomo", "--data-dir", `/Users/o'neil/Library/"Application Support"/$HOME/` + "`id`", "--uid", "501"}
	cmd := shellCommand(append([]string{"/usr/bin/printf", "%s\\n"}, args...))
	out, err := exec.Command("/bin/sh", "-c", cmd).Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if strings.Join(got, "\x00") != strings.Join(args, "\x00") {
		t.Fatalf("got %q want %q", got, args)
	}
}

// TestAppleScriptRoundTrip evaluates the generated AppleScript string
// literal with osascript (no privileges involved: we only return the
// string) and checks it decodes to the intended shell command.
func TestAppleScriptRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("osascript"); err != nil {
		t.Skip("no osascript")
	}
	shell := shellCommand([]string{"/x/lanflow-helper", "install", "--core", `/a "quoted" \ path/mihomo`, "--uid", "501"})
	script := appleScript(shell, "澜流需要安装权限组件以启用 TUN 模式")
	if !strings.HasPrefix(script, `do shell script "`) || !strings.Contains(script, `with administrator privileges with prompt "澜流`) {
		t.Fatalf("script = %s", script)
	}
	// Extract the first string literal and ask AppleScript to print it.
	lit := script[len("do shell script "):strings.Index(script, ` with administrator privileges`)]
	out, err := exec.Command("/usr/bin/osascript", "-e", "return "+lit).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSuffix(string(out), "\n"); got != shell {
		t.Fatalf("AppleScript decoded %q, want %q", got, shell)
	}
}

func TestAppleQuote(t *testing.T) {
	if got := appleQuote(`a"b\c`); got != `a\"b\\c` {
		t.Fatalf("got %s", got)
	}
}

func TestInstallBuildsOsascriptAndMapsCancel(t *testing.T) {
	dir := t.TempDir()
	helperBin := filepath.Join(dir, "lanflow-helper")
	coreBin := filepath.Join(dir, "mihomo")
	for _, p := range []string{helperBin, coreBin} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dataDir := filepath.Join(dir, "data dir")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}

	var gotName string
	var gotArgs []string
	oldExec, oldUID, oldVer := execCommand, currentUID, helperVersionOf
	t.Cleanup(func() { execCommand, currentUID, helperVersionOf = oldExec, oldUID, oldVer })
	helperVersionOf = func(context.Context, string) (string, error) { return "", errors.New("not a real helper") }
	currentUID = func() int { return 501 }
	execCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte("execution error: User canceled. (-128)\n"), errors.New("exit status 1")
	}

	err := install(context.Background(), InstallOptions{HelperBin: helperBin, CoreBin: coreBin, DataDir: dataDir})
	if !errors.Is(err, errCanceled) || err.Error() != "已取消授权" {
		t.Fatalf("err = %v", err)
	}
	if gotName != "/usr/bin/osascript" || len(gotArgs) != 2 || gotArgs[0] != "-e" {
		t.Fatalf("command = %s %v", gotName, gotArgs)
	}
	script := gotArgs[1]
	sum, _ := fileSHA256(helperBin)
	wantShell := verifiedExec(helperBin, sum, []string{"install", "--core", coreBin, "--data-dir", dataDir, "--uid", "501"})
	if !strings.Contains(script, appleQuote(wantShell)) {
		t.Fatalf("script %q does not contain %q", script, wantShell)
	}
	if !strings.Contains(script, "with administrator privileges with prompt \"澜流需要安装权限组件以启用 TUN 模式\"") {
		t.Fatalf("script = %s", script)
	}

	// Other failures surface the last line of the output.
	execCommand = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("lanflow-helper: 数据目录不属于用户 501: /x\n"), errors.New("exit status 1")
	}
	err = install(context.Background(), InstallOptions{HelperBin: helperBin, CoreBin: coreBin, DataDir: dataDir})
	if err == nil || !strings.Contains(err.Error(), "数据目录不属于用户") {
		t.Fatalf("err = %v", err)
	}

	// Validation failures never reach osascript.
	called := false
	execCommand = func(context.Context, string, ...string) ([]byte, error) { called = true; return nil, nil }
	if err := install(context.Background(), InstallOptions{HelperBin: "relative", CoreBin: coreBin, DataDir: dataDir}); err == nil || called {
		t.Fatalf("err = %v called = %v", err, called)
	}
}

func TestIsUserCanceled(t *testing.T) {
	if !isUserCanceled("execution error: User canceled. (-128)") || isUserCanceled("other error (-1)") {
		t.Fatal("cancel detection wrong")
	}
}

// fakeHelperScript writes a shell script that prints its arguments, one
// per line, standing in for lanflow-helper.
func fakeHelperScript(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "lanflow-helper")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestVerifiedExecRunsVerifiedCopy runs the generated script through
// /bin/sh (unprivileged): with the right digest the copy runs with the
// arguments intact; with a wrong digest, or when the source changes after
// the digest was taken, nothing runs.
func TestVerifiedExecRunsVerifiedCopy(t *testing.T) {
	if _, err := os.Stat("/private/var/tmp"); err != nil {
		t.Skip("no /private/var/tmp")
	}
	dir := t.TempDir()
	bin := fakeHelperScript(t, dir)
	sum, err := fileSHA256(bin)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"install", "--data-dir", `/Users/o'neil/$HOME/` + "`id`"}
	out, err := exec.Command("/bin/sh", "-c", verifiedExec(bin, sum, args)).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n"); strings.Join(got, "\x00") != strings.Join(args, "\x00") {
		t.Fatalf("got %q want %q", got, args)
	}

	// Swapped after hashing (the TOCTOU the copy closes): refused.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho pwned\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err = exec.Command("/bin/sh", "-c", verifiedExec(bin, sum, args)).CombinedOutput()
	if err == nil || strings.Contains(string(out), "pwned") || !strings.Contains(string(out), verifyFailed) {
		t.Fatalf("tampered helper ran: %v %s", err, out)
	}
}

func TestHelperDigestPinned(t *testing.T) {
	dir := t.TempDir()
	bin := fakeHelperScript(t, dir)
	sum, _ := fileSHA256(bin)
	old := ExpectedHelperSHA256
	t.Cleanup(func() { ExpectedHelperSHA256 = old })

	ExpectedHelperSHA256 = ""
	if got, err := helperDigest(bin); err != nil || got != sum {
		t.Fatalf("unpinned: %s %v", got, err)
	}
	ExpectedHelperSHA256 = strings.ToUpper(sum)
	if got, err := helperDigest(bin); err != nil || got != sum {
		t.Fatalf("pinned: %s %v", got, err)
	}
	ExpectedHelperSHA256 = strings.Repeat("0", 64)
	if _, err := helperDigest(bin); err == nil {
		t.Fatal("mismatching helper accepted")
	}
}
