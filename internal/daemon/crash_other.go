//go:build !windows

package daemon

import "os"

// redirectStderr 只在 Windows 上做(见 CaptureCrashes):Linux / macOS 的标准错误本来就有人收。
func redirectStderr(*os.File) bool { return false }
