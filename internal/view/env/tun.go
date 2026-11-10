package env

import (
	"errors"

	"lanflow/internal/app"
	"lanflow/internal/i18n"
)

// SetTUN turns TUN mode on or off from any page or the tray. The first time
// it is turned on without the privileged helper, it asks before installing
// the helper (administrator prompt) and then turns TUN on.
func SetTUN(e *Env, on bool) {
	if !on {
		e.Run(func() error { return e.API.SetTUN(false) }, i18n.T("TUN 模式已关闭"))
		return
	}
	if e.API.TUNHelperInstalled() {
		e.Run(func() error { return e.API.SetTUN(true) }, i18n.T("TUN 模式已开启"))
		return
	}
	e.Confirm(i18n.T("安装权限组件"), i18n.T("TUN 模式需要一个随系统运行的权限组件来创建虚拟网卡。安装时会请求管理员权限，只需一次。"), i18n.T("安装并开启"), false, func() {
		e.Run(func() error {
			if err := e.API.InstallTUNHelper(); err != nil {
				return err
			}
			err := e.API.SetTUN(true)
			if err == app.ErrHelperRequired {
				return errors.New(i18n.T("权限组件已安装，但暂时无法连接。请稍后再试一次"))
			}
			return err
		}, i18n.T("权限组件已安装，TUN 模式已开启"))
	})
}
