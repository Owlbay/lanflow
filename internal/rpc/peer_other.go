//go:build !windows && !darwin && !linux

package rpc

import "os"

// Elsewhere the 0700 directory and the 0600 socket keep other users out.
func sockPeerUID(int) (int, error) { return os.Getuid(), nil }
