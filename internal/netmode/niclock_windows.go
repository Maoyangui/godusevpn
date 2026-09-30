package netmode

import (
	"errors"
	"fmt"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 停用与还原网卡 IPv6 的跨进程互斥。守护进程(SYSTEM 服务)和「恢复网络」/ 卸载(提权的 godusevpn-svc)是两个进程,
// 各自读写同一份备份与待还原清单、改同一批绑定,进程内的锁管不到对方。没有这把锁时:还原刚把网卡开回去、
// 还没删备份,守护进程的巡检按它读到的旧备份又把网卡关上,随后还原删掉备份 —— 网卡 IPv6 关着、备份没了,
// 断开也还原不回来;两边同时写同一个 .tmp 还会互相抛错。
const nicLockName = `Global\godusevpn-nic-ipv6`

// nicLockSDDL 服务(SYSTEM)与提权的管理员进程都要能打开它,谁先建都一样。
const nicLockSDDL = "D:(A;;GA;;;SY)(A;;GA;;;BA)"

func withNICLock(fn func() error) error { return withNamedLock(nicLockName, 2*time.Minute, fn) }

// withNamedLock 拿着系统级具名互斥体跑 fn。互斥体归线程所有(放锁必须在同一条线程上),整段锁在当前 OS 线程上。
// 上一个持有者崩了(WAIT_ABANDONED)照样拿到:备份与清单都是整份换名写入的,不会留下半份。
func withNamedLock(name string, wait time.Duration, fn func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	sd, err := windows.SecurityDescriptorFromString(nicLockSDDL)
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	h, err := windows.CreateMutex(&sa, false, p) // 已经有了(ERROR_ALREADY_EXISTS)拿到的是同一个
	if h == 0 {
		return fmt.Errorf("网卡 IPv6:打不开跨进程锁: %w", err)
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, uint32(wait/time.Millisecond))
	switch ev {
	case windows.WAIT_OBJECT_0, windows.WAIT_ABANDONED:
	case uint32(windows.WAIT_TIMEOUT):
		return errors.New("网卡 IPv6:另一个进程正在改网卡的 IPv6,等了 " + wait.String() + " 还没完,稍后再试")
	default:
		return fmt.Errorf("网卡 IPv6:等跨进程锁失败: %v", err)
	}
	defer windows.ReleaseMutex(h)
	return fn()
}
