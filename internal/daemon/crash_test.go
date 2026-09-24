package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 子进程真的崩一次(没被 recover 的 goroutine panic),崩溃信息和启动抬头都要落进 logs/crash.log。
// 服务没有控制台,以前崩了什么都不剩。
func TestCaptureCrashesWritesPanic(t *testing.T) {
	if dir := os.Getenv("GODUSEVPN_CRASH_CHILD"); dir != "" {
		os.Setenv("GODUSEVPN_DATA", dir) // TestMain 已把它指到别处,这里改回父进程给的目录
		CaptureCrashes()
		go func() { panic("故意崩溃:crash.log 测试") }()
		select {}
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCaptureCrashesWritesPanic$", "-test.count=1")
	cmd.Env = append(os.Environ(), "GODUSEVPN_CRASH_CHILD="+dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("子进程应当崩溃退出,输出:\n%s", out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "logs", "crash.log"))
	if err != nil {
		t.Fatalf("没有 crash.log: %v\n子进程输出:\n%s", err, out)
	}
	s := string(b)
	for _, want := range []string{"启动 v", "故意崩溃:crash.log 测试", "goroutine"} {
		if !strings.Contains(s, want) {
			t.Fatalf("crash.log 里没有 %q:\n%s", want, s)
		}
	}
}
