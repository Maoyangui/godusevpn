//go:build !android

package daemon

import (
	"os"
	"path/filepath"
)

// selfProcess 运行内核的进程名(见 builder.Input.SelfProcess)。拿不到就返回空,节点规则按老样子不限定进程。
func selfProcess() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Base(exe)
}
