package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/paths"
)

// crashLogMax crash.log 超过这么大就在下次启动时挪成 crash.log.1(只留一份旧的):服务要是每次启动都崩、
// 被服务管理器反复拉起,每次一段栈,不管的话会一直涨。
const crashLogMax = 1 << 20

// crashFile Windows 服务模式下标准错误句柄指着它,不能被关、也不能被垃圾回收(os.File 的终结器会关句柄)。
var crashFile *os.File

// CaptureCrashes 让进程崩溃时的输出落进 logs/crash.log。服务 / 守护进程没有控制台,标准错误没人看:以前进程
// 崩了、被服务管理器几秒后拉起,现场什么都不剩。诊断包本来就会带上这个文件(diag.go)。
// 每次启动写一行抬头,崩溃记录前面那一行就能对上是哪次启动、哪个版本。
//
// service 为 true 且在 Windows 上:把进程的标准错误句柄直接指到这个文件。只靠 debug.SetCrashOutput 不够 ——
// fatal error(并发读写 map 等)的"fatal error: ..."那一行是在复制到崩溃文件之前就写出去的,文件里只剩栈、
// 没有原因;非 Go 代码(wintun、WFP 等 DLL)里的访问违例走 winthrow,一个字都不会复制过去。运行时每次写标准
// 错误都会重新取句柄,所以这两类都能收下;而 os.Stderr 在启动时就定下了(服务里是无效句柄),内核照常写
// 标准错误的日志不会灌进这个文件。
// 其余情形(Linux / macOS 的标准错误本来就进 journald / launchd 日志,前台 run 模式要留给控制台):用
// SetCrashOutput 另抄一份,原因行留在标准错误那边。
func CaptureCrashes(service bool) {
	dir := paths.Logs()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	p := filepath.Join(dir, "crash.log")
	rotateCrashLog(p)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "== %s 启动 v%s pid=%d\n", time.Now().Format("2006-01-02 15:04:05.000"), buildinfo.Version, os.Getpid())
	if service && redirectStderr(f) {
		crashFile = f
		return
	}
	// SetCrashOutput 自己复制了一份句柄(崩溃时写那一份),这里的 f 就可以关了
	_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	_ = f.Close()
}

// rotateCrashLog 超过 crashLogMax 就挪成 .1(os.Rename 在各平台都会覆盖更早的那份,Windows 上用的是 MOVEFILE_REPLACE_EXISTING)。
func rotateCrashLog(p string) {
	if st, err := os.Stat(p); err == nil && st.Size() > crashLogMax {
		_ = os.Rename(p, p+".1")
	}
}
