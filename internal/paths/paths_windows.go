// Package paths 各平台的文件位置。数据布局三端一致:
// settings.json、profiles/<id>.json、config.json、config.last-good.json、state.json、cache.db、logs/、rulesets/、diag/。
//
// Windows:一切都在 %ProgramData%\godusevpn 下。里面有节点凭据、订阅地址与面板密码哈希,
// 所以服务**每次启动**都会把目录权限收成只有 Administrators 与 SYSTEM 能进(见 Harden 与 secure_windows.go)——
// 不是只在安装时设一次:老版本装出来的目录、被别的程序改过权限的目录,都要在启动时修回来。
package paths

import (
	"os"
	"path/filepath"
)

// DataDir 服务数据目录。
func DataDir() string {
	if p := os.Getenv("GODUSEVPN_DATA"); p != "" {
		return p
	}
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, AppName)
}

// ConfDir 设置文件目录;Windows 上与数据目录相同。
func ConfDir() string { return DataDir() }
