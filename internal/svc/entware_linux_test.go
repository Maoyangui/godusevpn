package svc

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeEnv 设了它,测试程序就不跑测试,而是冒充 godusevpn(见 fakeGodusevpn)。
const fakeEnv = "GODUSEVPN_TEST_FAKE_DIR"

func TestMain(m *testing.M) {
	if dir := os.Getenv(fakeEnv); dir != "" {
		os.Exit(fakeGodusevpn(dir, os.Args[1:]))
	}
	os.Exit(m.Run())
}

// fakeGodusevpn 冒充 godusevpn 的进程(把测试程序复制成那个名字来跑,进程名、argv[0] 都和真的一样):
//   - run:守护进程。登记进程号(run.<pid>)后一直挂着,收到 TERM / KILL 就没了;
//   - 别的子命令:像 godusevpn uninstall / stop 那样调启动脚本(sh $FAKE_SCRIPT $FAKE_ACTION),
//     跑完把退出码和输出写进 caller.out —— 自己被脚本杀了就写不出来。
func fakeGodusevpn(dir string, args []string) int {
	if len(args) > 0 && args[0] == "run" {
		_ = os.WriteFile(filepath.Join(dir, "run."+strconv.Itoa(os.Getpid())), nil, 0o644)
		time.Sleep(10 * time.Minute) // 测试出了岔子也不会永远留着
		return 0
	}
	out, err := exec.Command("sh", os.Getenv("FAKE_SCRIPT"), os.Getenv("FAKE_ACTION")).CombinedOutput()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		code = -1
	}
	_ = os.WriteFile(filepath.Join(dir, "caller.out"), []byte(fmt.Sprintf("%d\n%s", code, out)), 0o644)
	return 0
}

// fakeRCFunc 照 Entware 的 /opt/etc/init.d/rc.func 写的替身:按进程名 pidof / killall;start 在后台拉起 "$PROC $ARGS",
// 等它出现;stop 先 killall 再等它没了;restart = 在跑就 stop,再 start。
const fakeRCFunc = `ACTION=$1
CALLER=$2
killall() { sig=TERM; case "$1" in -*) sig=${1#-}; shift ;; esac; p=$(pidof "$1"); [ -z "$p" ] || kill -$sig $p; }
start() {
	[ "$ENABLED" = yes ] || return 8
	if [ -n "$(pidof $PROC)" ]; then echo " already running."; return 0; fi
	$PREARGS $PROC $ARGS >/dev/null 2>&1 &
	i=0
	while [ -z "$(pidof $PROC)" ] && [ $i -le 10 ]; do sleep 1; i=$((i + 1)); done
	[ -n "$(pidof $PROC)" ] || return 255
}
stop() {
	case "$ACTION" in
	stop | restart) killall $PROC; i=0; while [ -n "$(pidof $PROC)" ] && [ $i -le 10 ]; do sleep 1; i=$((i + 1)); done ;;
	kill) killall -9 $PROC ;;
	esac
	[ -z "$(pidof $PROC)" ] || return 255
}
check() { [ -n "$(pidof $PROC)" ]; }
for PROC in $PROCS; do
	case $ACTION in
	start) start ;;
	stop | kill) check && stop ;;
	restart) check && stop; start ;;
	check) check ;;
	*) exit 1 ;;
	esac
done
`

// 梅林(Entware)上 godusevpn uninstall / stop 调启动脚本停服务,不能把正在调它的自己也杀了:
// 以前脚本的 stop 交给 rc.func,按名字 killall godusevpn,调用者也叫这个名字,卸载做到一半就没了。
// 真起几个同名进程(把测试程序复制成一个独有的名字,不碰机器上别的进程)跑一遍脚本:
// stop 只停 "名字 run" 那个守护进程、调用者活着拿到退出码 0;没在跑时也一样;kill 用 KILL;
// restart 停掉旧的、经 rc.func 拉起新的;最后用以前的做法(交给 rc.func)对照,确认替身 rc.func 确实会连调用者一起杀。
func TestEntwareStopSparesCaller(t *testing.T) {
	for _, tool := range []string{"sh", "pidof", "tr", "sed", "kill", "sleep"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("没有 %s", tool)
		}
	}
	if _, err := os.Stat("/proc/self/cmdline"); err != nil {
		t.Skip("没有 /proc")
	}
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	procName := "gvfake" + hex.EncodeToString(rnd[:]) // 14 个字符:进程名最长 15,超了 pidof 认不全
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(bin, procName)
	copyFile(t, self, fake)
	if err := os.WriteFile(filepath.Join(dir, "rc.func"), []byte(fakeRCFunc), 0o644); err != nil {
		t.Fatal(err)
	}
	stopped := filepath.Join(dir, "state", "stopped")
	script := filepath.Join(dir, "S99godusevpn")
	s := strings.Replace(entwareScript, "PROCS=godusevpn", "PROCS="+procName, 1)
	s = strings.Replace(s, "/opt/etc/init.d/rc.func", filepath.Join(dir, "rc.func"), 1)
	s = strings.Replace(s, "/opt/var/lib/godusevpn/stopped", stopped, 1)
	s = strings.Replace(s, "PATH=/opt/bin:", "PATH="+bin+":/opt/bin:", 1)
	if s == entwareScript || strings.Count(s, dir) != 3 {
		t.Fatalf("脚本模板变了,测试里的替换要跟上:\n%s", s)
	}
	if err := os.WriteFile(script, []byte(s), 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), fakeEnv+"="+dir, "FAKE_SCRIPT="+script)
	t.Cleanup(func() { // 冒充的守护进程一个都不留(只杀名字对得上、还活着的:进程号可能早被别的进程用了)
		for _, pid := range runPIDs(dir) {
			if fakeAlive(pid, procName) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	startDaemon := func() (*exec.Cmd, chan error) {
		t.Helper()
		before := len(runPIDs(dir))
		c := exec.Command(fake, "run")
		c.Env = env
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- c.Wait() }()
		waitFor(t, "守护进程登记进程号", func() bool { return len(runPIDs(dir)) > before })
		return c, done
	}
	caller := func(action string) (code int, out string, alive bool) {
		t.Helper()
		_ = os.Remove(filepath.Join(dir, "caller.out"))
		c := exec.Command(fake, "uninstall") // 和 godusevpn uninstall 一样:同一个名字,命令行第二项不是 run
		c.Env = append(env, "FAKE_ACTION="+action)
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- c.Wait() }()
		select {
		case <-done:
		case <-time.After(40 * time.Second):
			_ = c.Process.Kill()
			t.Fatalf("调用者跑 %s 超时", action)
		}
		b, err := os.ReadFile(filepath.Join(dir, "caller.out"))
		if err != nil {
			return 0, "", false // 被杀了,结果没写出来
		}
		first, rest, _ := strings.Cut(string(b), "\n")
		code, _ = strconv.Atoi(first)
		return code, rest, true
	}
	gone := func(done chan error, sig syscall.Signal) {
		t.Helper()
		select {
		case err := <-done:
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("守护进程应被信号结束: %v", err)
			}
			if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != sig {
				t.Fatalf("守护进程应被 %v 结束: %v", sig, err)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("守护进程没被停掉")
		}
	}
	marked := func() bool { _, err := os.Stat(stopped); return err == nil }

	// 1. godusevpn uninstall / stop 调脚本 stop:守护进程(TERM)停了,调用者活着、退出码 0,留下"用户停了"的标记
	_, done := startDaemon()
	code, out, alive := caller("stop")
	if !alive || code != 0 {
		t.Fatalf("调脚本 stop 的调用者应活着拿到退出码 0: alive=%v code=%d\n%s", alive, code, out)
	}
	gone(done, syscall.SIGTERM)
	if !marked() {
		t.Fatal("stop 要留下标记,看门狗才不拉")
	}

	// 2. 守护进程本来就没在跑(比如升级失败后来卸载):同样不碰调用者,直接成功
	if code, out, alive := caller("stop"); !alive || code != 0 {
		t.Fatalf("没在跑时 stop 也不该杀调用者: alive=%v code=%d\n%s", alive, code, out)
	}

	// 3. kill:用 KILL
	_, done = startDaemon()
	if code, out, alive := caller("kill"); !alive || code != 0 {
		t.Fatalf("kill 也不该杀调用者: alive=%v code=%d\n%s", alive, code, out)
	}
	gone(done, syscall.SIGKILL)

	// 4. restart(面板升级经 Restart 走这条):停掉旧的、去掉标记、经 rc.func 拉起新的(新的不是本测试的子进程,按登记的进程号找)
	_, done = startDaemon()
	old := runPIDs(dir)
	rs := exec.Command("sh", script, "restart")
	rs.Env = env
	rsOut, err := rs.CombinedOutput()
	if err != nil {
		t.Fatalf("restart: %v\n%s", err, rsOut)
	}
	gone(done, syscall.SIGTERM)
	if marked() {
		t.Fatal("restart 要去掉标记")
	}
	var fresh []int
	newDaemons := func() bool { // 新拉起的那个刚 exec 完时可能还没来得及登记,等一会儿
		fresh = fresh[:0]
		for _, pid := range runPIDs(dir) {
			if !containsInt(old, pid) && fakeAlive(pid, procName) {
				fresh = append(fresh, pid)
			}
		}
		return len(fresh) > 0
	}
	for i := 0; i < 100 && !newDaemons(); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if len(fresh) != 1 {
		t.Fatalf("restart 应经 rc.func 拉起一个新的守护进程: %v(旧的 %v)\n%s", fresh, old, rsOut)
	}

	// 5. 对照:以前的做法(stop 交给 rc.func,按名字 killall)会把调用者一起杀掉 —— 替身 rc.func 和真的一样会出这个事
	legacy := filepath.Join(dir, "legacy-stop")
	if err := os.WriteFile(legacy, []byte("ENABLED=yes\nPROCS="+procName+"\nARGS=run\n. "+filepath.Join(dir, "rc.func")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env = append(env, "FAKE_SCRIPT="+legacy) // 后写的同名变量优先
	if _, _, alive := caller("stop"); alive {
		t.Fatal("对照失败:交给 rc.func 的 stop 应该连调用者一起杀掉(替身和真的 rc.func 不一致?)")
	}
	waitFor(t, "对照里的守护进程也被 killall 掉", func() bool { return !fakeAlive(fresh[0], procName) })
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
}

// runPIDs 冒充的守护进程登记过的进程号(活着的、已经没了的都在)。
func runPIDs(dir string) []int {
	var out []int
	m, _ := filepath.Glob(filepath.Join(dir, "run.*"))
	for _, p := range m {
		if pid, err := strconv.Atoi(strings.TrimPrefix(filepath.Ext(p), ".")); err == nil {
			out = append(out, pid)
		}
	}
	return out
}

// fakeAlive pid 是不是还活着的冒充进程:/proc/<pid>/stat 里名字对得上、不是僵尸。
func fakeAlive(pid int, name string) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	st := string(b)
	l, r := strings.IndexByte(st, '('), strings.LastIndexByte(st, ')')
	if l < 0 || r < l || r+2 >= len(st) {
		return false
	}
	return st[l+1:r] == name && st[r+2] != 'Z'
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("等不到:%s", what)
}
