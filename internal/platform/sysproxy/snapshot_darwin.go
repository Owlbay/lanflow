//go:build darwin

package sysproxy

import "context"

func snapshot() (Saved, error) { return darwinSnapshot(context.Background(), runCommand) }

func restore(s Saved) error { return darwinRestore(context.Background(), runCommand, s) }
