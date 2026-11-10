//go:build !darwin && !windows

package sysproxy

import "errors"

var errUnsupported = errors.New("当前系统暂不支持设置系统代理，请在应用或浏览器中手动填写代理地址")

func set(string, int, []string) error { return errUnsupported }
func clear() error                    { return errUnsupported }
func setPAC(string) error             { return errUnsupported }
func get() (State, error)             { return State{}, errUnsupported }
