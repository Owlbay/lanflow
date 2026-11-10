//go:build darwin

package sysproxy

import "context"

func set(host string, port int, bypass []string) error {
	return darwinSet(context.Background(), runCommand, host, port, bypass)
}

func clear() error { return darwinClear(context.Background(), runCommand) }

func setPAC(url string) error { return darwinSetPAC(context.Background(), runCommand, url) }

func get() (State, error) { return darwinGet(context.Background(), runCommand) }
