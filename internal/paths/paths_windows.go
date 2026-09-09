// Package paths 各平台的文件位置。数据布局三端一致:
// settings.json、profiles/<id>.json、config.json、config.last-good.json、state.json、cache.db、logs/、rulesets/、diag/。
//
// Windows:一切都在 %ProgramData%\godusevpn 下。目录只给管理员与 SYSTEM 读写(安装时设 ACL),因为配置里有节点凭据。
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
