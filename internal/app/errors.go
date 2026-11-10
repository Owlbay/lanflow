package app

import "lanflow/internal/i18n"

// ErrHelperRequired is returned by SetTUN when the privileged helper is not
// installed; the UI then offers InstallTUNHelper. Its text follows the
// language of the moment (i18n.Error).
var ErrHelperRequired = i18n.Error("TUN 模式需要先安装权限组件")
