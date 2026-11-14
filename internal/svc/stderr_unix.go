//go:build linux || darwin

package svc

import (
	"log"
	"os"

	"golang.org/x/sys/unix"
)

// crashStderr 换掉 os.Stderr 之后,原来那个(fd 2)得有人引用着:os.File 有终结器,没人引用了垃圾回收就把 fd 2 关掉,
// 运行时崩溃时就写不进去了(fd 2 还可能被别的文件、连接占去,崩溃输出写进别处)。没换过是 nil。
var crashStderr *os.File

// CrashStderr 守护进程没起来、跑着出错退出时,原因写到这里:改过道就是原来的标准错误(系统日志、launchd 的日志文件、
// 梅林的崩溃记录文件),没改过就是 os.Stderr。这一行不带连接目标。
func CrashStderr() *os.File {
	if crashStderr != nil {
		return crashStderr
	}
	return os.Stderr
}

// quietStderr 守护进程平时也往标准错误写:内核(sing-box)在 info 级给每条连接记一行目标,写到它建日志那一刻的 os.Stderr。
// 由初始化系统拉起时标准错误有人收着落盘 —— systemd 收进 journald(很多发行版持久化)、OpenWrt 的 procd 收进 logread、
// macOS 的 launchd 写进 /Library/Logs/godusevpn.launchd.log(磁盘上、不轮转)、梅林接到崩溃记录文件 —— 而连接目标默认不落盘。
// 所以标准错误不是终端时,把 Go 这一层写标准错误的(os.Stderr、标准库 log)改去 /dev/null,返回 true;fd 2 本身不动:
// 运行时崩溃(fatal error、panic)照样直接写 fd 2,原因和栈交给系统日志,没起来的原因也写到那里(CrashStderr)。
// 依赖库在包初始化时就拿走的 os.Stderr(sing-box 的包级日志)这里管不到,见 daemon.QuietLibraryLogs。
// 在终端里前台跑的(排障)什么都不动,内核日志照旧打在终端上。
func quietStderr() bool {
	if crashStderr != nil {
		return true
	}
	if isTerminal(os.Stderr) {
		return false
	}
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	crashStderr = os.Stderr
	os.Stderr = null
	log.SetOutput(null)
	return true
}

// isTerminal f 是不是终端(能取到终端属性)。不用 f.Fd():它会把描述符改成阻塞的。
func isTerminal(f *os.File) bool {
	rc, err := f.SyscallConn()
	if err != nil {
		return false
	}
	var terr error
	if err := rc.Control(func(fd uintptr) { _, terr = unix.IoctlGetTermios(int(fd), ioctlReadTermios) }); err != nil {
		return false
	}
	return terr == nil
}
