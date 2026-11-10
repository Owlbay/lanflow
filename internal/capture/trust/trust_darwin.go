package trust

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"lanflow/internal/capture/ca"
)

func loginKeychain() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Keychains", "login.keychain-db")
}

func withPEM(der []byte, fn func(path string) error) error {
	f, err := os.CreateTemp("", "lanflow-ca-*.pem")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	f.Write(ca.EncodePEM(der))
	f.Close()
	return fn(f.Name())
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

func install(der []byte) error {
	return withPEM(der, func(p string) error {
		// user domain: macOS shows its own authorization dialog
		return run("/usr/bin/security", "add-trusted-cert", "-r", "trustRoot", "-p", "ssl", "-k", loginKeychain(), p)
	})
}

func remove(der []byte) error {
	err := withPEM(der, func(p string) error {
		return run("/usr/bin/security", "remove-trusted-cert", p)
	})
	derr := run("/usr/bin/security", "delete-certificate", "-Z", SHA1(der), loginKeychain())
	if err != nil && derr != nil {
		if strings.Contains(derr.Error(), "could not be found") || strings.Contains(derr.Error(), "Unable to delete") {
			return nil
		}
		return err
	}
	return nil
}

func find() []string {
	out, err := exec.Command("/usr/bin/security", "find-certificate", "-a", "-c", "Lanflow Local CA", "-Z", loginKeychain()).Output()
	if err != nil {
		return nil
	}
	var hs []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if h, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "SHA-1 hash: "); ok {
			hs = append(hs, strings.ToUpper(h))
		}
	}
	return hs
}
