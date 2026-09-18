package uiapi

import (
	"path/filepath"
	"strings"
)

// isRootBinary tar 成员是不是压缩包**根目录**下的守护进程。
// tar 打出来的名字有 "./godusevpn" 和 "godusevpn" 两种写法,都要认;
// 带目录的(godusevpn.app/Contents/MacOS/godusevpn)一律不认。
func isRootBinary(name string) bool {
	n := filepath.ToSlash(name)
	n = strings.TrimPrefix(n, "./")
	return n == "godusevpn"
}
