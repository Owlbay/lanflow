package rpc

import "time"

// SetConsentTimeout shortens the wait for the user in tests.
func SetConsentTimeout(d time.Duration) (restore func()) {
	old := consentTimeout
	consentTimeout = d
	return func() { consentTimeout = old }
}
