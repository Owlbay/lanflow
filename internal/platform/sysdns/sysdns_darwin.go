//go:build darwin

package sysdns

import "context"

const supported = true

func snapshot() (Saved, error) { return darwinSnapshot(context.Background(), runCommand) }

func set(servers []string) error { return darwinSet(context.Background(), runCommand, servers) }

func restore(s Saved, ours []string) error {
	return darwinRestore(context.Background(), runCommand, s, ours)
}
