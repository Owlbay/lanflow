package i18n

// English of the Mihomo core version management (internal/app/cores*.go,
// internal/view/settings/cores.go), kept apart from en.go so that it can
// be edited alongside the feature.
func init() {
	for k, v := range map[string]string{
		// service
		"未知的内核更新通道：%s":                        "Unknown core update channel: %s",
		"内核版本号格式不正确：%q":                       "Malformed core version: %q",
		"内置内核随应用安装，不能删除。":                     "The bundled core ships with the app and cannot be removed.",
		"这是正在使用的内核，请先切换到其他版本再删除。":             "This core is in use; switch to another version before removing it.",
		"内核 %s 未安装。":                          "Core %s is not installed.",
		"删除内核 %s 失败：%v":                       "Could not remove core %s: %v",
		"版本 %s 不属于所选的更新通道。":                   "Version %s does not belong to the selected channel.",
		"内核无法运行：%v":                           "The core cannot run: %v",
		"GitHub 接口的请求次数已达上限，请稍后再试":            "GitHub API rate limit reached; try again later",
		"GitHub 接口的请求次数已达上限，约 %d 分钟后恢复":       "GitHub API rate limit reached; it resets in about %d min",
		"Mihomo 内核 %s 可在「设置 › 内核」中安装；当前为 %s。": "Mihomo %s can be installed in Settings › Core; the current version is %s.",
		"内核自动更新失败":                            "Automatic core update failed",
		"自动更新已切换到新版本内核；旧版本仍保留在已安装列表中。":        "The automatic update switched to the new core; the previous version is kept in the installed list.",

		// 内核 tab
		"内核版本":     "Core version",
		"已安装的内核版本": "Installed core version",
		"应用自带的内核":  "The core shipped with the app",
		"安装于 ":     "installed ",
		"使用中":      "In use",
		"随应用安装":    "Ships with the app",
		"安装其他版本…":  "Install another version…",
		"安装其他版本":   "Install another version",
		"删除内核 %s？": "Remove core %s?",
		"内核文件会从磁盘删除；需要时可以重新安装。": "The core file is deleted from disk; it can be installed again when needed.",
		"已删除内核 %s":  "Core %s removed",
		"已切换到内核 %s": "Switched to core %s",
		"切换内核":      "Switch core",
		"切换":        "Switch",
		"已安装权限组件：切换后需要重新登记新内核，期间会请求一次管理员权限；内核会以同样的配置立即重新启动，系统代理保持不变。": "The helper is installed: the new core has to be registered with it, which asks for administrator rights once. The core restarts right away on the same configuration; the system proxy is left unchanged.",
		"内核更新": "Core updates",
		"每天检查一次所选通道有没有新内核；发现新版本时提醒，不会自行切换，除非打开自动安装": "Looks for a new core in the selected channel once a day; a new version is announced, never switched to on its own unless automatic install is on",
		"更新通道": "Update channel",
		"预发布（Alpha）跟随最新提交，功能更新但可能不稳定": "Prerelease (Alpha) follows the latest commit: newer features, possibly unstable",
		"自动安装更新": "Install updates automatically",
		"下载、校验并切换到新版本；旧版本保留在列表中": "Downloads, verifies and switches to the new version; the previous one stays in the list",
		"正式版": "Stable",
		"预发布": "Prerelease",
		"最新版本 %s 已安装，可在列表中点「使用」切换":               "The latest version %s is already installed; click Use in the list to switch",
		"内核 %s 已安装，可在列表中点「使用」切换":                 "Core %s installed; click Use in the list to switch",
		"预发布版本按最新提交构建，版本号是提交哈希；适合想第一时间用上新功能的用户。": "Prerelease builds follow the latest commit and are named after its hash, for users who want new features first.",
		"下载进度":      "Download progress",
		"正在读取版本列表…": "Loading the release list…",
		"没有可用的版本":   "No versions available",
		"读取中…":      "Loading…",
		"已安装":       "Installed",
		"安装":        "Install",
	} {
		en[k] = v
	}
}
