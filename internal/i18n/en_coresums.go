package i18n

// English for the checksum check of downloaded cores
// (internal/app/coresums.go). Kept in its own file, apart from en.go,
// which another owner maintains.
func init() {
	for k, v := range map[string]string{
		"无法从 GitHub 取得内核的 SHA-256 校验值，未下载：%v": "Could not get the core's SHA-256 checksum from GitHub, so nothing was downloaded: %v",
		"GitHub 没有提供 %s 的 SHA-256 校验值，为安全起见不安装这个版本；请换一个版本，或点击「选择内核文件」使用自己核对过的内核。": "GitHub provides no SHA-256 checksum for %s, so this version is not installed; pick another version, or click “Choose Core File” to use a core you have checked yourself.",
		"GitHub 上的这个版本没有 %s":   "This release on GitHub has no %s",
		"无法计算下载文件的 SHA-256：%v": "Could not compute the SHA-256 of the download: %v",
		"内核文件 %s 校验失败：SHA-256 为 %s，应为 %s。文件可能在下载途中（例如经镜像）被替换，已丢弃，没有运行或安装。": "Checksum check of %s failed: its SHA-256 is %s, expected %s. The file may have been replaced on the way (for example by a mirror); it was discarded without being run or installed.",
	} {
		en[k] = v
	}
}
