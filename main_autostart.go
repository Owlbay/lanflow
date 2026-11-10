package main

import "lanflow/internal/view"

// applyAutostart makes the login item follow Settings.OpenAtLogin.
func applyAutostart(_ string, open bool) { view.ApplyOpenAtLogin(open) }
