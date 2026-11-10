//go:build !darwin && !windows

package atrust

import "context"

func detect(context.Context, func(string) bool) Info { return Info{} }
