// macOS:守护进程以 root 由 launchd 拉起,按系统惯例放在 /Library/Application Support 下;
// 设置与数据分两层(godusevpn/ 与 godusevpn/data/),布局其余部分与 Windows / Linux 一致。
// 环境变量 GODUSEVPN_CONF / GODUSEVPN_DATA 可整体覆盖(测试与非 root 运行用)。

package paths

import (
	"os"
	"path/filepath"
)

const appSupport = "/Library/Application Support/" + AppName

// DataDir 数据目录:缓存、规则集、日志、诊断包。
func DataDir() string {
	if p := os.Getenv("GODUSEVPN_DATA"); p != "" {
		return p
	}
	return filepath.Join(appSupport, "data")
}

// ConfDir 设置文件目录。
func ConfDir() string {
	if p := os.Getenv("GODUSEVPN_CONF"); p != "" {
		return p
	}
	return appSupport
}
