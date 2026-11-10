package i18n

// English of the two-process app (docs/service-split.md S2/S3): the
// window's connection to the service (main_split.go,
// internal/app/remote) and the service's tray consent items
// (internal/view/background.go).
func init() {
	for k, v := range map[string]string{
		"与澜流后台服务的连接已断开，正在重新连接": "Lost the connection to Lanflow's background service; reconnecting",
		"已重新连接澜流后台服务":          "Reconnected to Lanflow's background service",
		"待授权：%s…":              "Waiting for permission: %s…",
		"需要重启澜流后台服务":           "Lanflow's background service needs a restart",
		"后台服务的版本（%s）与界面（%s）不一致，通常是更新后旧的后台服务还在运行。重启后台服务时内核会短暂停止。": "The background service (%s) and the window (%s) are different versions, usually because the old service kept running after an update. Restarting the service stops the core briefly.",
		"重启后台服务":                    "Restart service",
		"无法启动澜流后台服务: %w":            "Could not start Lanflow's background service: %w",
		"澜流以单进程方式运行，已显示它自己的窗口":      "Lanflow runs as a single process and showed its own window",
		"另一个旧版本的澜流正在运行：请先退出它，再打开澜流": "An older version of Lanflow is running: quit it first, then open Lanflow again.",
	} {
		en[k] = v
	}
}
