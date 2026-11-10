//go:build !darwin && !windows

package helper

import (
	"context"
	"errors"
)

var errUnsupported = errors.New("当前平台不支持权限组件")

func install(context.Context, InstallOptions) error { return errUnsupported }
func uninstall(context.Context) error               { return errUnsupported }
