package i18n

// English of configuration sync: lanflow.yaml's errors
// (internal/syncfile) and the service's sync messages (internal/app).
func init() {
	for k, v := range map[string]string{
		// internal/syncfile/codec.go
		"第 %d 行（%s）：": "Line %d (%s): ",
		"第 %d 行：":     "Line %d: ",
		"：":           ": ",
		"应为时间（如 2026-01-02T15:04:05Z）": "should be a time (like 2026-01-02T15:04:05Z)",
		"应为键值对象":                       "should be a mapping",
		"重复的键":                         "duplicate key",
		"未知的键，已忽略":                     "unknown key, ignored",
		"应为列表":                         "should be a list",
		"应为单个值":                        "should be a single value",
		"应为 true 或 false":              "should be true or false",
		"应为整数":                         "should be an integer",
		"应为非负整数":                       "should be a non-negative integer",
		"应为数字":                         "should be a number",
		// internal/syncfile/doc.go
		"文件是空的":             "the file is empty",
		"文件顶层应为键值对象":        "the file's top level should be a mapping",
		"缺少 format（文件格式版本）": "format (the file format version) is missing",
		"format 应为正整数":      "format should be a positive integer",
		"文件由更新版本的澜流写入（格式 %d），当前版本只能读取格式 %d": "the file was written by a newer Lanflow (format %d); this version reads format %d only",
		"YAML 语法错误：": "YAML syntax error: ",
		"订阅 ID 只能由字母、数字、- 和 _ 组成（最多 64 个字符）": "a subscription ID may only hold letters, digits, - and _ (at most 64 characters)",
		"订阅 ID 重复": "duplicate subscription ID",
		"订阅名称不能为空": "the subscription name cannot be empty",
		"订阅链接应为 keychain:<id> 引用或 http(s) 链接": "the subscription link should be a keychain:<id> reference or an http(s) link",
		"keychain: 后面缺少名称":                    "the name after keychain: is missing",
		"自动更新间隔和超时不能为负数":                      "the update interval and timeout cannot be negative",
		"覆写 ID 只能由字母、数字、- 和 _ 组成（最多 64 个字符）":  "an override ID may only hold letters, digits, - and _ (at most 64 characters)",
		"覆写 ID 重复": "duplicate override ID",
		"覆写链接应以 http:// 或 https:// 开头":  "the override link should start with http:// or https://",
		"覆写内容不是有效的 YAML：":               "the override is not valid YAML: ",
		"覆写「%s」不在 overrides 列表中":        "override \"%s\" is not in the overrides list",
		"自定义规则需要 type、payload 和 target": "a custom rule needs type, payload and target",

		// internal/app/configdoc.go
		"文件无效：%v": "The file is not valid: %v",
		"订阅「%s」（文件中只有钥匙串引用，本机钥匙串里没有它的链接）": "subscription \"%s\" (the file has only a keychain reference, and this machine's keychain does not have its link)",

		// internal/app/configsecrets.go, internal/view/subs/page.go
		"这个订阅来自配置同步，本机钥匙串里没有它的链接。请在「编辑」中重新输入订阅链接。": "This subscription came through configuration sync, and this machine's keychain does not have its link. Enter the subscription link again in Edit.",
		"需要重新输入链接": "Link needed",

		// internal/app/configapply.go
		"与本机一个从文件导入的订阅 ID 相同":  "is the ID of a subscription imported from a file on this machine",
		"保存配置失败，可能只加载了一部分：%v":  "Saving the configuration failed; it may be loaded only in part: %v",
		"文件与本机配置一致，没有需要加载的内容。": "The file matches this machine's configuration; there is nothing to load.",
		"更新了%s": "Updated %s",
		"；%d 个订阅需要重新输入链接": "; %d subscription(s) need their link entered again",
		"；正在使用的订阅「%s」已保留": "; the subscription in use, \"%s\", was kept",

		// internal/app/configsync.go
		"同步文件夹中已有不同的 lanflow.yaml：请选择使用文件中的配置，还是用本机配置覆盖文件。": "The sync folder already has a different lanflow.yaml: choose whether to use the configuration in the file or to overwrite the file with this machine's.",
		"读取同步文件失败：%v":    "Reading the sync file failed: %v",
		"已加载同步的配置":       "Synced configuration loaded",
		"同步文件未加载，配置保持不变": "Sync file not loaded; the configuration is unchanged",
		"无法创建同步文件夹：%v":   "Cannot create the sync folder: %v",
		"写入同步文件失败：%v":    "Writing the sync file failed: %v",
		"同步文件有冲突副本":      "The sync file has conflict copies",
		"同步文件夹里有 %d 个冲突副本（%s）。请在「设置 › 常规 › 配置同步」中选择保留哪一个；澜流不会自动删除任何副本。": "The sync folder has %d conflict copy(ies) (%s). Choose which to keep in Settings › General › Configuration sync; Lanflow never deletes a copy by itself.",
		"没有找到 iCloud 云盘文件夹。请在系统设置中登录 iCloud 并打开 iCloud 云盘。":             "No iCloud Drive folder found. Sign in to iCloud in System Settings and turn on iCloud Drive.",
		"请选择同步文件夹。": "Choose a sync folder.",
		"iCloud 正在下载同步文件夹中已有的 lanflow.yaml，请稍后再试。": "iCloud is downloading the lanflow.yaml already in the sync folder; try again in a moment.",
		"同步文件无效，无法使用：%v":                           "The sync file is not valid and cannot be used: %v",
		"同步文件中的配置未能加载：%v":                          "The configuration in the sync file could not be loaded: %v",
		"备份原有同步文件失败：%v":                            "Backing up the existing sync file failed: %v",
		"配置同步未开启。":                                 "Configuration sync is off.",
		"不是同步文件的冲突副本。":                             "Not a conflict copy of the sync file.",
		"读取冲突副本失败：%v":                              "Reading the conflict copy failed: %v",
		"冲突副本无效：%v":                                "The conflict copy is not valid: %v",
		"冲突副本中的配置未能加载：%v":                          "The configuration in the conflict copy could not be loaded: %v",
		"移走冲突副本失败：%v":                              "Moving the conflict copy away failed: %v",
	} {
		en[k] = v
	}
}
