// Package sysdns points the system DNS at a resolver that the TUN captures
// and puts the previous servers back afterwards.
//
// Why: on macOS the system resolver usually talks to a server on the local
// subnet (the router). Traffic to the local subnet never enters the TUN, so
// the core cannot hijack those DNS queries: domain rules then rely on
// sniffing alone and fake-ip does not work. Pointing the system DNS at a
// public address makes the queries go through the TUN, where the core
// answers them.
package sysdns

import "errors"

// TUNServers is what the system DNS is set to while TUN runs. Any address
// outside the local subnet works (the core hijacks every query on port 53);
// a real public resolver keeps name resolution working if the core dies
// before the setting can be put back.
var TUNServers = []string{"223.5.5.5", "119.29.29.29"}

// ErrUnsupported is returned on platforms without an implementation.
var ErrUnsupported = errors.New("当前系统不支持自动设置系统 DNS")

// Saved is an opaque snapshot of the system DNS servers.
type Saved []byte

// Supported reports whether this platform can change the system DNS.
func Supported() bool { return supported }

// Snapshot captures the current DNS servers of every enabled network service.
func Snapshot() (Saved, error) { return snapshot() }

// Set points every enabled network service at servers.
func Set(servers []string) error { return set(servers) }

// Restore puts back the servers saved in s, but only on services that still
// use ours (a service someone changed in the meantime is left alone).
func Restore(s Saved, ours []string) error { return restore(s, ours) }
