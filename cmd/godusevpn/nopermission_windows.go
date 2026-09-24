//go:build windows

package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// noPermissionHint 控制口拒绝这个账户时提示的种类,以及界面能不能替用户登记。
// Windows:控制管道有一份账户名单,界面可以提权跑 register-controller --restart 把本账户加进去。
func noPermissionHint() (string, bool) { return "register", true }

var (
	modShell32          = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW = modShell32.NewProc("ShellExecuteExW")
)

// shellExecuteInfo SHELLEXECUTEINFOW,字段顺序与对齐照 C 的来(64 位上共 112 字节)。
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         uintptr
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
)

// registerController 提权跑 register-controller --restart 并**等它跑完**,按退出码报结果。
// 用户在 UAC 里点了"否"、提权进程失败、三分钟还没跑完,都如实报错,不再一律报成功。
func registerController(svcExe string) error {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(svcExe)
	params, _ := windows.UTF16PtrFromString("register-controller --restart")
	dir, _ := windows.UTF16PtrFromString(filepath.Dir(svcExe))
	info := shellExecuteInfo{fMask: seeMaskNoCloseProcess | seeMaskNoAsync, lpVerb: verb, lpFile: file, lpParameters: params, lpDirectory: dir, nShow: windows.SW_HIDE}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if r, _, e := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); r == 0 {
		if errors.Is(e, windows.ERROR_CANCELLED) {
			return errors.New("E_REGISTER_FAILED: 没有同意管理员权限")
		}
		return fmt.Errorf("E_REGISTER_FAILED: %v", e)
	}
	if info.hProcess == 0 {
		return errors.New("E_REGISTER_FAILED: 没拿到提权进程")
	}
	defer windows.CloseHandle(info.hProcess)
	ev, err := windows.WaitForSingleObject(info.hProcess, 180*1000)
	if err != nil {
		return fmt.Errorf("E_REGISTER_FAILED: %v", err)
	}
	if ev != windows.WAIT_OBJECT_0 {
		return errors.New("E_REGISTER_FAILED: 三分钟还没跑完")
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.hProcess, &code); err != nil {
		return fmt.Errorf("E_REGISTER_FAILED: %v", err)
	}
	if code != 0 {
		return fmt.Errorf("E_REGISTER_FAILED: 退出码 %d", code)
	}
	return nil
}
