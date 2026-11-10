//go:build !darwin && !windows

package sysproxy

import "errors"

func snapshot() (Saved, error) { return nil, errors.New("当前系统暂不支持设置系统代理") }

func restore(Saved) error { return errors.New("当前系统暂不支持设置系统代理") }
