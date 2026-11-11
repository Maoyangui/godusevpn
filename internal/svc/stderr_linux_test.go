package svc

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openTerminal 开一对伪终端(主、从),主端的输出留给调用者读(openPTY 会把它读掉);开不了就跳过。
func openTerminal(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("开不了伪终端: %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		m.Close()
		t.Skipf("开不了伪终端: %v", err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		m.Close()
		t.Skipf("开不了伪终端: %v", err)
	}
	s, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		t.Skipf("开不了伪终端: %v", err)
	}
	return m, s
}

// waitKept 等冒充的守护进程 pid 处理完标准错误,返回它改了没有(true / false)。
func waitKept(t *testing.T, r *entwareRig, pid int) string {
	t.Helper()
	f := filepath.Join(r.dir, "stderr."+strconv.Itoa(pid))
	waitFor(t, "守护进程处理完标准错误", func() bool { _, err := os.Stat(f); return err == nil })
	b, _ := os.ReadFile(f)
	return string(b)
}

// runOnTerminal 像在终端里前台跑 godusevpn run 那样(标准输入输出、标准错误都是终端)起冒充的守护进程,
// 等它处理完标准错误、写完连接目标就杀掉;返回终端上出现的内容和它改没改标准错误。
func runOnTerminal(t *testing.T, r *entwareRig, env ...string) (out, kept string) {
	t.Helper()
	m, s := openTerminal(t)
	defer m.Close()
	c := exec.Command(r.fake, "run")
	c.Env = r.envWith(env...)
	c.Stdin, c.Stdout, c.Stderr = s, s, s
	if err := c.Start(); err != nil {
		s.Close()
		t.Fatal(err)
	}
	s.Close()
	var mu sync.Mutex
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 4096)
		for {
			n, err := m.Read(b)
			mu.Lock()
			buf.Write(b[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	kept = waitKept(t, r, c.Process.Pid)
	if kept == "false" { // 终端上的内容要读到连接目标那几行(读到了才能说"照旧出在终端上")
		for i := 0; i < 100; i++ {
			mu.Lock()
			got := strings.Contains(buf.String(), "secret-target-stderr.example")
			mu.Unlock()
			if got {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	_ = c.Process.Kill()
	_ = c.Wait()
	m.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	mu.Lock()
	defer mu.Unlock()
	return buf.String(), kept
}

// 守护进程平时往标准错误写:内核(sing-box)在 info 级给每条连接记一行目标。systemd 把服务的标准错误收进 journald
// (很多发行版落盘)、OpenWrt 的 procd 收进 logread、macOS 上 launchd 写进 /Library/Logs/godusevpn.launchd.log
// (磁盘上、不轮转)—— 连接目标默认不落盘,所以只要标准错误不是终端(由初始化系统拉起),守护进程一启动就把 Go 这一层
// 写标准错误的(os.Stderr、标准库 log)改去 /dev/null;原来的 fd 2 留着,运行时崩溃(fatal error、panic)时的原因和栈、
// 守护进程没起来的原因照旧写到那里,交给系统日志。在终端里前台跑的(排障)什么都不改。
// 用真的 sing-box 日志、标准库 log、os.Stderr 各写一行连接目标:标准错误接到普通文件(launchd)、管道(procd、journald
// 那一头)时都不出现,崩溃原因和没起来的原因照样在;接在终端上时照旧出现。
func TestStderrQuietAsService(t *testing.T) {
	r := newEntwareRig(t, "gvquiet", false)
	keep := "FAKE_KEEP=1"
	// 1. 标准错误接到普通文件(像 launchd 的 StandardErrorPath):连接目标不进文件;崩了原因和栈在
	logPath := filepath.Join(r.dir, "launchd.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(r.fake, "run")
	c.Env = r.envWith(keep)
	c.Stderr = f
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	if k := waitKept(t, r, c.Process.Pid); k != "true" {
		t.Fatalf("标准错误接着文件(不是终端)时要改去 /dev/null(得 %s)", k)
	}
	read := func() string { b, _ := os.ReadFile(logPath); return string(b) }
	if s := read(); strings.Contains(s, "secret-target") {
		t.Fatalf("连接目标写进了标准错误接的文件:\n%s", s)
	}
	_ = syscall.Kill(c.Process.Pid, syscall.SIGUSR1)
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		_ = c.Process.Kill()
		t.Fatal("守护进程没崩")
	}
	s := read()
	if !strings.Contains(s, "fatal error: sync: unlock of unlocked mutex") || !strings.Contains(s, "goroutine ") {
		t.Fatalf("崩溃的原因和栈要留在原来的标准错误上:\n%s", s)
	}
	if strings.Contains(s, "secret-target") {
		t.Fatalf("崩了之后连接目标也不该在:\n%s", s)
	}

	// 2. 标准错误接着管道(procd、systemd 收日志的那一头):同样不出现;没起来的原因照样写到原来的标准错误
	var pipe bytes.Buffer
	c = exec.Command(r.fake, "run")
	c.Env = r.envWith(keep, "FAKE_FAIL=1")
	c.Stderr = &pipe
	if err := c.Run(); exitCode(err) != 1 {
		t.Fatalf("没起来的守护进程应以 1 退出: %v\n%s", err, pipe.String())
	}
	if s := pipe.String(); strings.Contains(s, "secret-target") || !strings.Contains(s, "失败: 建数据目录:") {
		t.Fatalf("管道上不该有连接目标,要有没起来的原因:\n%s", s)
	}

	// 3. 在终端里前台跑:什么都不改,连接目标照旧出在终端上(排障时看得到内核日志)
	out, k := runOnTerminal(t, r, keep)
	if k != "false" {
		t.Fatalf("标准错误是终端时不该改(得 %s)", k)
	}
	for _, w := range []string{"secret-target-singbox.example", "secret-target-stdlog.example", "secret-target-stderr.example"} {
		if !strings.Contains(out, w) {
			t.Fatalf("终端上应照旧有 %s:\n%s", w, out)
		}
	}

}
