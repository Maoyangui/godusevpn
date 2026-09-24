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

// CaptureCrashes 让 Go 侧的崩溃(没被 recover 的 panic、并发读写 map 这类 fatal error)另写一份到 logs/crash.log。
// 服务 / 守护进程没有控制台,标准错误没人看:以前进程崩了、被服务管理器几秒后拉起,现场什么都不剩,
// 只能从"控制管道突然连不上、几秒后又好了"去猜。诊断包本来就会带上这个文件(diag.go)。
// 每次启动写一行抬头,崩溃记录前面那一行就能对上是哪次启动、哪个版本。
func CaptureCrashes() {
	dir := paths.Logs()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "crash.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		_ = f.Close()
		return
	}
	// SetCrashOutput 自己复制了一份句柄(崩溃时写那一份),这里的 f 写完抬头就关
	fmt.Fprintf(f, "== %s 启动 v%s pid=%d\n", time.Now().Format("2006-01-02 15:04:05.000"), buildinfo.Version, os.Getpid())
	_ = f.Close()
}
