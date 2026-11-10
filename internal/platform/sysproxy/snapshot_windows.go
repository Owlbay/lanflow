//go:build windows

package sysproxy

import (
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/sys/windows/registry"
)

func snapshot() (Saved, error) {
	k, err := openSettings(registry.QUERY_VALUE)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	saved := windowsSaved{OS: "windows"}
	if v, _, err := k.GetIntegerValue("ProxyEnable"); err == nil {
		saved.Enable = &v
	} else if !errors.Is(err, registry.ErrNotExist) {
		return nil, fmt.Errorf("读取 ProxyEnable 失败：%w", err)
	}
	if v, _, err := k.GetStringValue("ProxyServer"); err == nil {
		saved.Server = &v
	}
	if v, _, err := k.GetStringValue("ProxyOverride"); err == nil {
		saved.Override = &v
	}
	if v, _, err := k.GetStringValue("AutoConfigURL"); err == nil {
		saved.AutoConfig = &v
	}
	saved.HasAutoConfig = true
	return json.Marshal(saved)
}

func restore(s Saved) error {
	var saved windowsSaved
	if err := json.Unmarshal(s, &saved); err != nil || saved.OS != "windows" {
		return errors.New("保存的系统代理设置无法识别")
	}
	k, err := openSettings(registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	putString := func(name string, v *string) error {
		if v == nil {
			if err := k.DeleteValue(name); err != nil && !errors.Is(err, registry.ErrNotExist) {
				return err
			}
			return nil
		}
		return k.SetStringValue(name, *v)
	}
	if err := putString("ProxyServer", saved.Server); err != nil {
		return fmt.Errorf("恢复 ProxyServer 失败：%w", err)
	}
	if err := putString("ProxyOverride", saved.Override); err != nil {
		return fmt.Errorf("恢复 ProxyOverride 失败：%w", err)
	}
	if saved.HasAutoConfig {
		if err := putString("AutoConfigURL", saved.AutoConfig); err != nil {
			return fmt.Errorf("恢复 AutoConfigURL 失败：%w", err)
		}
	}
	enable := uint32(0)
	if saved.Enable != nil {
		enable = uint32(*saved.Enable)
	}
	if err := k.SetDWordValue("ProxyEnable", enable); err != nil {
		return fmt.Errorf("恢复 ProxyEnable 失败：%w", err)
	}
	return notifySettingsChanged()
}
