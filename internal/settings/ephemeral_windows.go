package settings

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// ephemeralErr Windows 上等一下就好的文件错误:
//   - 拒绝访问:名字处在"删除挂起"(删掉了但还有句柄开着,CreateFile 在这个名字上一律拒绝访问);
//     改名替换、删除碰上别的进程开着它且没给 FILE_SHARE_DELETE(Go 的 os.Open 就不给)
//   - 共享冲突、锁冲突:杀毒软件、索引服务正开着它
//   - 删除挂起:少数接口把它原样报出来
//
// Go 工具链在 Windows 上对同样几种错误也是重试(cmd/internal/robustio)。
func ephemeralErr(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case windows.ERROR_ACCESS_DENIED, windows.ERROR_SHARING_VIOLATION, windows.ERROR_LOCK_VIOLATION, windows.ERROR_DELETE_PENDING:
		return true
	}
	return false
}
