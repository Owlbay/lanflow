// Package trust installs and removes the capture root in the user's own
// trust store (docs/capture.md §6): the login Keychain on macOS
// (`security add-trusted-cert -r trustRoot -p ssl`), CurrentUser\Root on
// Windows (the system asks the user to confirm). Only the window process
// calls it; whether the root is trusted is decided by verifying a leaf it
// signed against the system roots, never by an exit code (MDM / GPO may
// make a call "succeed" without effect).
package trust

import (
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"strings"

	"lanflow/internal/capture/ca"
)

// Trusted reports whether the system accepts leaf (signed by the root).
func Trusted(leaf *x509.Certificate) bool {
	if leaf == nil {
		return false
	}
	return ca.TrustCheck(leaf, nil) == nil
}

// Install adds the root (DER) to the user's trust store.
func Install(der []byte) error { return install(der) }

// Remove deletes the root (DER) from the user's trust store; a root not
// there is not an error.
func Remove(der []byte) error { return remove(der) }

// Leftovers lists the SHA-1 (hex, upper case) of Lanflow roots in the
// user's store other than keep (DER; nil = all).
func Leftovers(keep []byte) []string {
	all := find()
	if keep == nil {
		return all
	}
	k := SHA1(keep)
	var out []string
	for _, h := range all {
		if !strings.EqualFold(h, k) {
			out = append(out, h)
		}
	}
	return out
}

// SHA1 is the certificate hash the stores index by.
func SHA1(der []byte) string {
	s := sha1.Sum(der)
	return strings.ToUpper(hex.EncodeToString(s[:]))
}
