package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCrashChild 只在子进程里跑:按 GODUSEVPN_CRASH_KIND 真的崩一次。
func TestCrashChild(t *testing.T) {
	if os.Getenv("GODUSEVPN_CRASH_CHILD") == "" {
		t.Skip("只在 crash 测试的子进程里跑")
	}
	service := os.Getenv("GODUSEVPN_CRASH_SERVICE") == "1"
	CaptureCrashes(service)
	if service {
		// 先让垃圾回收和终结器跑几遍再崩:crash.log 的 *os.File 要是没被包级变量引用住,终结器会关掉
		// 标准错误句柄,后面的崩溃输出就丢了。服务是长期运行的,这一步一定会发生。
		for i := 0; i < 3; i++ {
			runtime.GC()
			time.Sleep(30 * time.Millisecond)
		}
	}
	switch os.Getenv("GODUSEVPN_CRASH_KIND") {
	case "panic":
		go func() { panic("故意崩溃:crash.log 测试") }()
		select {}
	case "fatal":
		var mu sync.Mutex
		mu.Unlock() // 运行时的 fatal error,不是 panic,recover 不了
	case "native":
		crashInNativeCode()
	}
	t.Fatal("没崩")
}

// runCrashChild 起一个子进程崩一次,返回它写下的 crash.log。
func runCrashChild(t *testing.T, kind string, service bool) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashChild$", "-test.count=1")
	svcFlag := "0"
	if service {
		svcFlag = "1"
	}
	cmd.Env = append(os.Environ(), "GODUSEVPN_CRASH_CHILD="+dir, "GODUSEVPN_CRASH_KIND="+kind, "GODUSEVPN_CRASH_SERVICE="+svcFlag)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("子进程应当崩溃退出,输出:\n%s", out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "logs", "crash.log"))
	if err != nil {
		t.Fatalf("没有 crash.log: %v\n子进程输出:\n%s", err, out)
	}
	if !strings.Contains(string(b), "启动 v") {
		t.Fatalf("crash.log 里没有启动抬头:\n%s", b)
	}
	return string(b)
}

func mustContain(t *testing.T, s string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Fatalf("crash.log 里没有 %q:\n%s", w, s)
		}
	}
}

// 非服务模式(前台 run、Linux / macOS):SetCrashOutput 另抄一份。panic 连原因带栈都有。
func TestCaptureCrashesPanic(t *testing.T) {
	mustContain(t, runCrashChild(t, "panic", false), "panic: 故意崩溃:crash.log 测试", "goroutine")
}

// fatal error(并发读写 map、解锁没锁的互斥锁……)走的是另一条路:非服务模式下原因那一行在复制开始前就写出去了,
// 文件里只有栈 —— 至少栈得在,脚本据此认出崩溃(原因在 journald / launchd 那边的标准错误里)。
func TestCaptureCrashesFatalKeepsStack(t *testing.T) {
	mustContain(t, runCrashChild(t, "fatal", false), "goroutine")
}

// crash.log 超过上限就挪成 .1(已有的 .1 被覆盖),没超过不动。服务每次启动都崩时文件不会一直涨。
func TestRotateCrashLog(t *testing.T) {
	p := filepath.Join(t.TempDir(), "crash.log")
	if err := os.WriteFile(p, []byte("small"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotateCrashLog(p)
	if _, err := os.Stat(p + ".1"); err == nil {
		t.Fatal("没超过上限不该挪")
	}
	if err := os.WriteFile(p+".1", []byte("older"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, make([]byte, crashLogMax+1), 0o600); err != nil {
		t.Fatal(err)
	}
	rotateCrashLog(p)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("超过上限应当挪走,原文件还在:%v", err)
	}
	if st, err := os.Stat(p + ".1"); err != nil || st.Size() != crashLogMax+1 {
		t.Fatalf(".1 应当是刚挪过去的那份(覆盖旧的):%v %v", st, err)
	}
}
