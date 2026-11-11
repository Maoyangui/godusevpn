package daemon

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sblog "github.com/sagernet/sing-box/log"

	"github.com/Maoyangui/godusevpn/internal/paths"
)

// TestQuietLibraryLogsChild 只在子进程里跑:sing-box 的包级日志先写一行对照,QuietLibraryLogs 之后再写连接目标。
func TestQuietLibraryLogsChild(t *testing.T) {
	if os.Getenv("GODUSEVPN_QUIET_CHILD") == "" {
		t.Skip("只在 QuietLibraryLogs 测试的子进程里跑")
	}
	sblog.Info("before-quiet-marker")
	QuietLibraryLogs()
	sblog.Info("inbound connection to secret-target-std.example:443")
	sblog.StdLogger().Warn("outbound connection to secret-target-std2.example:443")
}

// sing-box 的日志包初始化时就建了一个包级日志,拿的是那一刻的 os.Stderr(原来的 fd 2)。守护进程起来后把 os.Stderr
// 改去 /dev/null(svc.KeepStderrForCrashes)管不到它:内核里用包级日志(log.Info 之类)写的东西照旧进原来的标准错误 ——
// 由初始化系统拉起时那就是系统日志、launchd 的日志文件、梅林的崩溃记录文件。QuietLibraryLogs 把它也换成不输出的。
// 子进程的标准错误接到文件:对照那一行在(说明确实写得进去),之后写的连接目标不在。
func TestQuietLibraryLogs(t *testing.T) {
	out := filepath.Join(t.TempDir(), "stderr")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestQuietLibraryLogsChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "GODUSEVPN_QUIET_CHILD=1")
	cmd.Stderr = f
	runErr := cmd.Run()
	f.Close()
	b, _ := os.ReadFile(out)
	if runErr != nil {
		t.Fatalf("子进程: %v\n%s", runErr, b)
	}
	if !strings.Contains(string(b), "before-quiet-marker") {
		t.Fatalf("对照那一行应写进了标准错误(不然这个测试测不出东西):\n%s", b)
	}
	if strings.Contains(string(b), "secret-target") {
		t.Fatalf("QuietLibraryLogs 之后 sing-box 的包级日志还在往原来的标准错误写:\n%s", b)
	}
}

// 守护进程没起来(建数据目录失败、控制管道被占着……)或者跑着返回了错误:原因记进 crash.log,和崩溃一样诊断包会带上
// (Windows 服务没有能看的标准错误,以前这个原因哪儿都不留,只剩系统日志里一个退出码)。
func TestRecordFailure(t *testing.T) {
	p := filepath.Join(paths.Logs(), "crash.log")
	_ = os.Remove(p)
	RecordFailure(errors.New("监听控制管道: address already in use"))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("没记进 crash.log: %v", err)
	}
	if !strings.Contains(string(b), "监听控制管道: address already in use") {
		t.Fatalf("crash.log 里要有没起来的原因:\n%s", b)
	}
	if ex := crashExcerpt(p); !strings.Contains(ex, "监听控制管道: address already in use") {
		t.Fatalf("诊断包摘的那一段要带上它:\n%s", ex)
	}
	if st, err := os.Stat(p); err == nil && st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("crash.log 只给本人看,得 %o", st.Mode().Perm())
	}
	RecordFailure(nil) // 没出错什么都不记
	if b2, _ := os.ReadFile(p); string(b2) != string(b) {
		t.Fatalf("没出错不该记:\n%s", b2)
	}
}
