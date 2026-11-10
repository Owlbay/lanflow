package trust

import (
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"lanflow/internal/capture/ca"
)

// Untested on a real Windows machine (docs/capture.md §15).

func openRoot() (windows.Handle, error) {
	name, _ := windows.UTF16PtrFromString("ROOT")
	h, err := windows.CertOpenStore(windows.CERT_STORE_PROV_SYSTEM, 0, 0,
		windows.CERT_SYSTEM_STORE_CURRENT_USER, uintptr(unsafe.Pointer(name)))
	if err != nil {
		return 0, fmt.Errorf("open CurrentUser\\Root: %w", err)
	}
	return h, nil
}

func install(der []byte) error {
	st, err := openRoot()
	if err != nil {
		return err
	}
	defer windows.CertCloseStore(st, 0)
	// Windows asks the user to confirm adding a root to CurrentUser\Root.
	c, err := windows.CertCreateCertificateContext(windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING, &der[0], uint32(len(der)))
	if err != nil {
		return err
	}
	defer windows.CertFreeCertificateContext(c)
	return windows.CertAddCertificateContextToStore(st, c, windows.CERT_STORE_ADD_REPLACE_EXISTING, nil)
}

func each(fn func(c *windows.CertContext, der []byte) bool) error {
	st, err := openRoot()
	if err != nil {
		return err
	}
	defer windows.CertCloseStore(st, 0)
	var prev *windows.CertContext
	for {
		c, err := windows.CertEnumCertificatesInStore(st, prev)
		if err != nil || c == nil {
			return nil
		}
		der := unsafe.Slice(c.EncodedCert, c.Length)
		prev = c
		if !fn(c, append([]byte(nil), der...)) {
			windows.CertFreeCertificateContext(c)
			return nil
		}
	}
}

func remove(der []byte) error {
	want := SHA1(der)
	var derr error
	each(func(c *windows.CertContext, d []byte) bool {
		if SHA1(d) != want {
			return true
		}
		dup := windows.CertDuplicateCertificateContext(c)
		derr = windows.CertDeleteCertificateFromStore(dup) // frees dup
		return false
	})
	if derr != nil && !errors.Is(derr, windows.ERROR_NOT_FOUND) {
		return derr
	}
	return nil
}

func find() []string {
	var out []string
	each(func(_ *windows.CertContext, d []byte) bool {
		if c, err := x509.ParseCertificate(d); err == nil && ca.IsOurs(c) && strings.HasPrefix(c.Subject.CommonName, "Lanflow Local CA") {
			out = append(out, SHA1(d))
		}
		return true
	})
	return out
}
