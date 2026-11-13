//go:build !windows

package app

import (
	"os"
	"testing"
	"time"
)

func TestParseEtime(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"00:05":      5 * time.Second,
		"12:34":      12*time.Minute + 34*time.Second,
		"01:02:03":   time.Hour + 2*time.Minute + 3*time.Second,
		"2-01:00:00": 49 * time.Hour,
		"bad":        -1,
		"1:2:3:4":    -1,
		"x-00:01":    -1,
	} {
		got, ok := parseEtime(in)
		if want < 0 {
			if ok {
				t.Errorf("%q parsed as %s", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%q = %s, %v; want %s", in, got, ok, want)
		}
	}
}

func TestParseLsof(t *testing.T) {
	if got := parseLsof([]byte("p4321\ncClashX Meta\nf7\n")); got != "ClashX Meta (PID 4321)" {
		t.Fatalf("%q", got)
	}
	if got := parseLsof(nil); got != "" {
		t.Fatalf("%q", got)
	}
}

// The real check: a process that does not run capture-agent, or started
// after the record, is not taken for one.
func TestIsCaptureAgentReal(t *testing.T) {
	sl := startSleeper(t)
	if isCaptureAgent(sl.pid(), time.Now().Add(time.Hour)) {
		t.Fatal("the test binary was taken for a decryption process")
	}
	if isCaptureAgent(os.Getpid(), time.Now()) {
		t.Fatal("this process was taken for a decryption process")
	}
	agent := startSleeper(t, "capture-agent") // runs `… capture-agent`
	time.Sleep(100 * time.Millisecond)
	if !isCaptureAgent(agent.pid(), time.Now()) {
		t.Fatal("a decryption process was not recognised")
	}
	if isCaptureAgent(agent.pid(), time.Now().Add(-time.Hour)) {
		t.Fatal("a process started after the record (a reused PID) was taken for the recorded one")
	}
}
