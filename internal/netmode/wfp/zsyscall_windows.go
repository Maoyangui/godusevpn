// 由 wireguard-windows 的 go generate 生成后手改:错误码改按返回值(见 fwpmProviderAdd0 里的注释)。

package wfp

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var _ unsafe.Pointer

var (
	modfwpuclnt = windows.NewLazySystemDLL("fwpuclnt.dll")

	procFwpmEngineClose0          = modfwpuclnt.NewProc("FwpmEngineClose0")
	procFwpmEngineOpen0           = modfwpuclnt.NewProc("FwpmEngineOpen0")
	procFwpmFilterAdd0            = modfwpuclnt.NewProc("FwpmFilterAdd0")
	procFwpmFreeMemory0           = modfwpuclnt.NewProc("FwpmFreeMemory0")
	procFwpmGetAppIdFromFileName0 = modfwpuclnt.NewProc("FwpmGetAppIdFromFileName0")
	procFwpmProviderAdd0          = modfwpuclnt.NewProc("FwpmProviderAdd0")
	procFwpmSubLayerAdd0          = modfwpuclnt.NewProc("FwpmSubLayerAdd0")
	procFwpmTransactionAbort0     = modfwpuclnt.NewProc("FwpmTransactionAbort0")
	procFwpmTransactionBegin0     = modfwpuclnt.NewProc("FwpmTransactionBegin0")
	procFwpmTransactionCommit0    = modfwpuclnt.NewProc("FwpmTransactionCommit0")
)

func fwpmEngineClose0(engineHandle uintptr) (err error) {
	r1, _, _ := syscall.Syscall(procFwpmEngineClose0.Addr(), 1, uintptr(engineHandle), 0, 0)
	if r1 != 0 {
		err = syscall.Errno(r1) // FWP 的 API 返回的是错误码本身,不是 GetLastError
	}
	return
}

func fwpmEngineOpen0(serverName *uint16, authnService wtRpcCAuthN, authIdentity *uintptr, session *wtFwpmSession0, engineHandle unsafe.Pointer) (err error) {
	r1, _, _ := syscall.Syscall6(procFwpmEngineOpen0.Addr(), 5, uintptr(unsafe.Pointer(serverName)), uintptr(authnService), uintptr(unsafe.Pointer(authIdentity)), uintptr(unsafe.Pointer(session)), uintptr(engineHandle), 0)
	if r1 != 0 {
		err = syscall.Errno(r1) // FWP 的 API 返回的是错误码本身,不是 GetLastError
	}
	return
}

func fwpmFilterAdd0(engineHandle uintptr, filter *wtFwpmFilter0, sd uintptr, id *uint64) (err error) {
	r1, _, _ := syscall.Syscall6(procFwpmFilterAdd0.Addr(), 4, uintptr(engineHandle), uintptr(unsafe.Pointer(filter)), uintptr(sd), uintptr(unsafe.Pointer(id)), 0, 0)
	if r1 != 0 {
		err = syscall.Errno(r1) // FWP 的 API 返回的是错误码本身,不是 GetLastError
	}
	return
}

func fwpmFreeMemory0(p unsafe.Pointer) {
	syscall.Syscall(procFwpmFreeMemory0.Addr(), 1, uintptr(p), 0, 0)
	return
}

func fwpmGetAppIdFromFileName0(fileName *uint16, appID unsafe.Pointer) (err error) {
	r1, _, _ := syscall.Syscall(procFwpmGetAppIdFromFileName0.Addr(), 2, uintptr(unsafe.Pointer(fileName)), uintptr(appID), 0)
	if r1 != 0 {
		err = syscall.Errno(r1) // FWP 的 API 返回的是错误码本身,不是 GetLastError
	}
	return
}

func fwpmProviderAdd0(engineHandle uintptr, provider *wtFwpmProvider0, sd uintptr) (err error) {
	r1, _, _ := syscall.Syscall(procFwpmProviderAdd0.Addr(), 3, uintptr(engineHandle), uintptr(unsafe.Pointer(provider)), uintptr(sd))
	if r1 != 0 {
		err = syscall.Errno(r1) // FWP 的 API 返回的是错误码本身,不是 GetLastError
	}
	return
}

func fwpmSubLayerAdd0(engineHandle uintptr, subLayer *wtFwpmSublayer0, sd uintptr) (err error) {
	r1, _, _ := syscall.Syscall(procFwpmSubLayerAdd0.Addr(), 3, uintptr(engineHandle), uintptr(unsafe.Pointer(subLayer)), uintptr(sd))
	if r1 != 0 {
		err = syscall.Errno(r1) // FWP 的 API 返回的是错误码本身,不是 GetLastError
	}
	return
}

func fwpmTransactionAbort0(engineHandle uintptr) (err error) {
	r1, _, _ := syscall.Syscall(procFwpmTransactionAbort0.Addr(), 1, uintptr(engineHandle), 0, 0)
	if r1 != 0 {
		err = syscall.Errno(r1) // FWP 的 API 返回的是错误码本身,不是 GetLastError
	}
	return
}

func fwpmTransactionBegin0(engineHandle uintptr, flags uint32) (err error) {
	r1, _, _ := syscall.Syscall(procFwpmTransactionBegin0.Addr(), 2, uintptr(engineHandle), uintptr(flags), 0)
	if r1 != 0 {
		err = syscall.Errno(r1) // FWP 的 API 返回的是错误码本身,不是 GetLastError
	}
	return
}

func fwpmTransactionCommit0(engineHandle uintptr) (err error) {
	r1, _, _ := syscall.Syscall(procFwpmTransactionCommit0.Addr(), 1, uintptr(engineHandle), 0, 0)
	if r1 != 0 {
		err = syscall.Errno(r1) // FWP 的 API 返回的是错误码本身,不是 GetLastError
	}
	return
}
