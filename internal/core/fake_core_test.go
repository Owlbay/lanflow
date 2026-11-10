package core

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as a fake Mihomo when LANFLOW_FAKE_CORE is set,
// so runner / version / validate can be exercised without the real core and
// without changing anything on the machine.
//
// Behaviour of the fake (argument driven, like the real one):
//
//	-v               print a realistic version banner, exit 0
//	-t ...           print a failing config test (logrus style), exit 1
//	(anything else)  print a few log lines, then either exit with
//	                 LANFLOW_FAKE_EXIT after LANFLOW_FAKE_EXIT_AFTER_MS, or
//	                 wait for an interrupt (SIGINT / CTRL_BREAK) and exit 0.
const (
	fakeCoreEnv       = "LANFLOW_FAKE_CORE"
	fakeExitEnv       = "LANFLOW_FAKE_EXIT"
	fakeExitAfterEnv  = "LANFLOW_FAKE_EXIT_AFTER_MS"
	fakeVersionBanner = "Mihomo Meta v1.19.33 darwin arm64 with go1.26.10 Wed Oct 21 16:44:27 UTC 2026\nUse tags: with_gvisor\n"
	fakeValidateOut   = `time="2026-11-08T23:53:53.411461000+08:00" level=info msg="Start initial configuration in progress"
time="2026-11-08T23:53:53.411624000+08:00" level=error msg="proxy group[0]: g: 'xx' not found"
configuration file /tmp/x/bad.yaml test failed
`
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeCoreEnv) == "" {
		os.Exit(m.Run())
	}
	os.Exit(runFakeCore(os.Args[1:]))
}

func runFakeCore(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "-v":
			fmt.Print(fakeVersionBanner)
			return 0
		case "-t":
			fmt.Print(fakeValidateOut)
			return 1
		}
	}
	fmt.Printf("time=\"%s\" level=info msg=\"fake core started\"\n", time.Now().Format(time.RFC3339Nano))
	fmt.Printf("time=\"%s\" level=info msg=\"args: %s\"\n", time.Now().Format(time.RFC3339Nano), strings.Join(args, " "))
	if code := os.Getenv(fakeExitEnv); code != "" {
		after, _ := strconv.Atoi(os.Getenv(fakeExitAfterEnv))
		time.Sleep(time.Duration(after) * time.Millisecond)
		fmt.Fprintf(os.Stderr, "time=\"%s\" level=error msg=\"fake core failing: listen tcp :7890: address already in use\"\n", time.Now().Format(time.RFC3339Nano))
		n, _ := strconv.Atoi(code)
		return n
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	fmt.Printf("time=\"%s\" level=info msg=\"fake core shutting down\"\n", time.Now().Format(time.RFC3339Nano))
	return 0
}

// fakeCoreSpec returns a Spec that runs this test binary as the core, plus
// the environment that makes it behave as the fake.
func fakeCoreSpec(t *testing.T, extraEnv ...string) Spec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := dir + string(os.PathSeparator) + "config.yaml"
	if err := os.WriteFile(cfg, []byte("mixed-port: 7890\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return Spec{
		Bin:        exe,
		WorkDir:    dir,
		Config:     cfg,
		Controller: shortSocketPath(t),
		LogFile:    dir + string(os.PathSeparator) + "core.log",
		Env:        append([]string{fakeCoreEnv + "=1"}, extraEnv...),
	}
}

// fakeBinary returns the path of this test binary and sets the fake env on
// the test process so plain exec.Command children inherit it.
func fakeBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeCoreEnv, "1")
	return exe
}
