// Package autostart 托盘客户端的登录自启:写当前用户的 Run 键,不需要管理员权限。
// 后台服务是另一回事(安装时注册为自动启动的系统服务),这里只管托盘。
package autostart

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	runKey    = `Software\Microsoft\Windows\CurrentVersion\Run`
	valueName = "godusevpn"
)

// Enabled 当前用户是否已设置登录自启(且指向当前可执行文件)。
func Enabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(valueName)
	if err != nil {
		return false
	}
	exe, _ := os.Executable()
	return strings.Contains(strings.ToLower(v), strings.ToLower(exe))
}

// Set 开 / 关登录自启;开时以 --minimized 启动,只到托盘不弹窗。
func Set(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !on {
		err := k.DeleteValue(valueName)
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return k.SetStringValue(valueName, `"`+exe+`" --minimized`)
}
