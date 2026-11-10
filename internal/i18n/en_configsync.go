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
	} {
		en[k] = v
	}
}
