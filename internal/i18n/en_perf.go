package i18n

// English of the performance work: 「清理缓存」 in 设置 › 数据
// (internal/view/settings/caches.go, internal/app/caches.go).
func init() {
	for k, v := range map[string]string{
		// ---- internal/app/caches.go ----
		"图标":        "Icons",
		"内核版本列表":    "Core release lists",
		"内核缓存":      "Core cache",
		"旧日志":       "Rotated logs",
		"清理缓存失败：%v": "Failed to clean the caches: %v",

		// ---- internal/view/settings/caches.go ----
		"缓存":       "Caches",
		"清理缓存":     "Clean Caches",
		"已清理 %s":   "Cleaned %s",
		"没有可清理的缓存": "Nothing to clean",
		"共 %s":     "%s in total",
	} {
		en[k] = v
	}
}
