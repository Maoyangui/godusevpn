package wfp

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// 生成的 zsyscall_windows.go 里没有删过滤器这一个,手写补上(签名与 FwpmFilterDeleteById0 一致)。
var procFwpmFilterDeleteById0 = modfwpuclnt.NewProc("FwpmFilterDeleteById0")

func fwpmFilterDeleteById0(engineHandle uintptr, id uint64) (err error) {
	r0, _, _ := syscall.SyscallN(procFwpmFilterDeleteById0.Addr(), engineHandle, uintptr(id))
	if r0 != 0 {
		err = syscall.Errno(r0)
	}
	return
}

// windows_GUID 给本包自己写的文件用的别名,免得每处都写全名。
type windows_GUID = windows.GUID
