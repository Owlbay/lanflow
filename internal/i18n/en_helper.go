package i18n

// English for the "helper of another version" prompts
// (internal/app/service.go refreshHelper, internal/view/env/tun.go,
// overview and settings pages).
func init() {
	for k, v := range map[string]string{
		"需要重新安装权限组件": "Helper needs reinstalling",
		"已安装的权限组件（%s）与澜流自带的版本（%s）不一致，TUN 模式暂不可用。请在「设置 › 代理」中重新安装权限组件。": "The installed helper (%s) does not match the one that comes with Lanflow (%s), so TUN Mode is unavailable. Reinstall the helper in Settings › Proxy.",
		"已安装的权限组件与这个版本的澜流不兼容，TUN 模式暂不可用。请在「设置 › 代理」中重新安装权限组件。":         "The installed helper does not work with this version of Lanflow, so TUN Mode is unavailable. Reinstall the helper in Settings › Proxy.",
		"重新安装权限组件": "Reinstall Helper",
		"已安装的权限组件来自另一个版本的澜流，需要换成这个版本自带的权限组件后才能使用 TUN 模式。安装时会请求管理员权限。": "The installed helper comes from another version of Lanflow. TUN Mode needs the helper that comes with this version. Installing asks for administrator rights.",
		"重新安装":      "Reinstall",
		"权限组件已重新安装": "Helper reinstalled",
		"已安装的权限组件来自另一个版本的澜流，TUN 模式暂不可用，需要重新安装权限组件。": "The installed helper comes from another version of Lanflow, so TUN Mode is unavailable until the helper is reinstalled.",
		"重新安装…": "Reinstall…",
	} {
		en[k] = v
	}
}
