//go:build !darwin && !windows

package app

import "context"

const helperLogDir = "/var/log/lanflow-helper"

func osVersion() string { return "" }

func systemDiagnostics(context.Context) map[string]string { return nil }
