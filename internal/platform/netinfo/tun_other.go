//go:build !darwin && !windows

package netinfo

import "context"

func otherTUN(context.Context) (string, bool) { return "", false }
