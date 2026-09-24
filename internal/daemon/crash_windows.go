package daemon

import (
	"os"

	"golang.org/x/sys/windows"
)

// redirectStderr 把进程的标准错误句柄换成 f(见 CaptureCrashes)。
func redirectStderr(f *os.File) bool {
	return windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd())) == nil
}
