//go:build windows

package sysproxy

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const internetSettingsKey = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

const (
	internetOptionRefresh         = 37 // INTERNET_OPTION_REFRESH
	internetOptionSettingsChanged = 39 // INTERNET_OPTION_SETTINGS_CHANGED
)

var (
	wininet                = windows.NewLazySystemDLL("wininet.dll")
	procInternetSetOptionW = wininet.NewProc("InternetSetOptionW")
)

func openSettings(access uint32) (registry.Key, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsKey, access)
	if err != nil {
		return 0, fmt.Errorf("无法打开 Internet 设置注册表项：%w", err)
	}
	return k, nil
}

func set(host string, port int, bypass []string) error {
	if host == "" || port <= 0 || port > 65535 {
		return fmt.Errorf("代理地址无效：%s:%d", host, port)
	}
	k, err := openSettings(registry.SET_VALUE | registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetStringValue("ProxyServer", formatProxyServer(host, port)); err != nil {
		return fmt.Errorf("写入 ProxyServer 失败：%w", err)
	}
	if err := k.SetStringValue("ProxyOverride", formatProxyOverride(bypass)); err != nil {
		return fmt.Errorf("写入 ProxyOverride 失败：%w", err)
	}
	if err := k.SetDWordValue("ProxyEnable", 1); err != nil {
		return fmt.Errorf("写入 ProxyEnable 失败：%w", err)
	}
	return notifySettingsChanged()
}

func clear() error {
	k, err := openSettings(registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetDWordValue("ProxyEnable", 0); err != nil {
		return fmt.Errorf("关闭系统代理失败：%w", err)
	}
	return notifySettingsChanged()
}

// setPAC writes (or, for "", deletes) the WinINet AutoConfigURL value,
// which is how Windows stores the PAC address; the manual proxy values
// are left as they are.
func setPAC(url string) error {
	k, err := openSettings(registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if url == "" {
		if err := k.DeleteValue("AutoConfigURL"); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("关闭 PAC 代理失败：%w", err)
		}
		return notifySettingsChanged()
	}
	if err := k.SetStringValue("AutoConfigURL", url); err != nil {
		return fmt.Errorf("写入 AutoConfigURL 失败：%w", err)
	}
	return notifySettingsChanged()
}

func get() (State, error) {
	k, err := openSettings(registry.QUERY_VALUE)
	if err != nil {
		return State{}, err
	}
	defer k.Close()
	var st State
	if enable, _, err := k.GetIntegerValue("ProxyEnable"); err == nil {
		st.Enabled = enable != 0
	} else if !errors.Is(err, registry.ErrNotExist) {
		return State{}, fmt.Errorf("读取 ProxyEnable 失败：%w", err)
	}
	if server, _, err := k.GetStringValue("ProxyServer"); err == nil {
		st.Host, st.Port = parseProxyServer(server)
	} else if !errors.Is(err, registry.ErrNotExist) {
		return State{}, fmt.Errorf("读取 ProxyServer 失败：%w", err)
	}
	if override, _, err := k.GetStringValue("ProxyOverride"); err == nil {
		st.Bypass = parseProxyOverride(override)
	} else if !errors.Is(err, registry.ErrNotExist) {
		return State{}, fmt.Errorf("读取 ProxyOverride 失败：%w", err)
	}
	if pac, _, err := k.GetStringValue("AutoConfigURL"); err == nil {
		st.PACURL = pac
	}
	return st, nil
}

// notifySettingsChanged tells WinINet (and through it most applications)
// that the proxy settings changed, then asks it to reload them.
func notifySettingsChanged() error {
	if err := procInternetSetOptionW.Find(); err != nil {
		return fmt.Errorf("系统代理已写入注册表，但无法加载 wininet.dll 通知系统刷新：%w", err)
	}
	for _, opt := range []uintptr{internetOptionSettingsChanged, internetOptionRefresh} {
		r, _, e := procInternetSetOptionW.Call(0, opt, 0, 0)
		if r == 0 {
			return fmt.Errorf("系统代理已写入注册表，但通知系统刷新失败（选项 %d）：%v", opt, e)
		}
	}
	return nil
}
