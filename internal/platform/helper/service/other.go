//go:build !darwin && !windows

package service

import (
	"context"
	"errors"
)

// Install locations are undefined on this platform.
const (
	LogDir  = "/var/log/lanflow-helper"
	LogPath = LogDir + "/helper.log"
)

var errUnsupported = errors.New("当前平台不支持权限组件")

// Install is unsupported on this platform.
func Install(context.Context, InstallOptions, Logf) error { return errUnsupported }

// Uninstall is unsupported on this platform.
func Uninstall(context.Context, Logf) error { return errUnsupported }

// Run is unsupported on this platform.
func Run(context.Context, string, Logf) error { return errUnsupported }
