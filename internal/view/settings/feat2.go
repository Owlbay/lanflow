package settings

import "lanflow/internal/model"

// feat2UI is the page state of the second feature batch: the shortcut
// rows (hotkey.go), the Wi-Fi rules (wifi.go), the download mirror
// (mirror.go) and the WebDAV backup (backup.go). It hangs off Page as
// one field so the page struct needs no further changes.
type feat2UI struct {
	// 全局快捷键: registration / conflict error under each row, by action,
	// and the actions whose row is shown (those with keys, plus the ones
	// added through the 添加快捷键 row this session)
	hotkeyErrs  map[string]string
	hotkeyShown map[string]bool

	// 按 Wi-Fi 切换订阅: the add-rule form
	wifiSSID  string
	wifiSub   string // subscription name chosen in the add form
	wifiErr   string
	wifiAdded int // bumps the add form's key so it clears

	// 下载镜像
	mirrorCustom     field
	mirrorCustomMode bool // 自定义… chosen: the URL field shows even with no mirror saved yet
	mirrorErr        string
	mirrorTesting    bool
	mirrorResults    []model.MirrorTest

	// 备份与同步: the 配置… dialog and the 管理备份… dialog
	davOpen                 bool
	davListOpen             bool
	davURL, davUser, davDir field
	davPass                 string
	davPassSession          int // bumps the password field's key after a save
	davErr                  string
	davBusy                 string // "test", "backup", "list", "restore", "delete" while a job runs
	davListed               bool
	davBackups              []model.WebDAVBackup
}
