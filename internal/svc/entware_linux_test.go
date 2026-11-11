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
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
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
//   - 设了 FAKE_QUERY:像 godusevpn status 那样问服务在不在跑。把自己的进程号、entwareDaemons(FAKE_QUERY)
//     (QueryStatus 现在的判断)和只按名字 pidof 的结果(以前的判断)写进 query.out,各占一行;
//   - idle:不相干的同名进程(像开着的 godusevpn logs -f),只是挂着,收到信号就没了;
//   - 别的子命令:像 godusevpn install / start / uninstall / stop 那样调启动脚本(sh $FAKE_SCRIPT $FAKE_ACTION),
//     跑完把退出码和输出写进 caller.out —— 自己被脚本杀了就写不出来。
func fakeGodusevpn(dir string, args []string) int {
	if len(args) > 0 && args[0] == "run" {
		_ = os.WriteFile(filepath.Join(dir, "run."+strconv.Itoa(os.Getpid())), nil, 0o644)
		time.Sleep(10 * time.Minute) // 测试出了岔子也不会永远留着
		return 0
	}
	if len(args) > 0 && args[0] == "idle" {
		time.Sleep(10 * time.Minute)
		return 0
	}
	if q := os.Getenv("FAKE_QUERY"); q != "" {
		byName, _ := exec.Command("pidof", q).Output()
		res := fmt.Sprintf("%d\n%s\n%s\n", os.Getpid(), strings.Trim(fmt.Sprint(entwareDaemons(q)), "[]"), strings.TrimSpace(string(byName)))
		_ = os.WriteFile(filepath.Join(dir, "query.out"), []byte(res), 0o644)
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
// 等它出现;stop 先 killall 再等它没了;restart = 在跑就 stop,再 start;reconfigure = killall -SIGHUP(按名字全发)。
const fakeRCFunc = `ACTION=$1
CALLER=$2
killall() { sig=TERM; case "$1" in -*) sig=${1#-}; sig=${sig#SIG}; shift ;; esac; p=$(pidof "$1"); [ -z "$p" ] || kill -$sig $p; }
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
	reconfigure) killall -SIGHUP $PROC ;;
	*) exit 1 ;;
	esac
done
`

// busyboxApplets 梅林上脚本用到的这些都是 BusyBox 的;BusyBox 那一遍把它们换成测试机上的 busybox。
var busyboxApplets = []string{"sh", "pidof", "tr", "sed", "kill", "sleep", "mkdir", "touch", "rm"}

// eachToolset 用系统自带的 sh / pidof / tr / sed … 跑一遍,再换成 BusyBox 的跑一遍(梅林上就是这些;没装 busybox 就跳过那一遍)。
func eachToolset(t *testing.T, f func(t *testing.T, busybox bool)) {
	t.Run("系统工具", func(t *testing.T) { f(t, false) })
	t.Run("BusyBox", func(t *testing.T) { f(t, true) })
}

// entwareRig 真跑 Entware 启动脚本的替身环境(都在一个临时目录里,不碰机器上别的进程和文件):
//   - bin/<name>:测试程序复制成一个独有的名字冒充 godusevpn(守护进程是 "名字 run",调用者是 "名字 install" 之类)。
//     名字 14 个字符:进程名最长 15,超了 pidof 认不全;
//   - rc.func:照 Entware 的 rc.func 写的替身(fakeRCFunc),每回被调先在 rc.log 记一笔动作;
//   - bin/cru、bin/logger:只记账的假货(cru.log、logger.log)。BusyBox 那一遍 sh 自己有 logger(applet 优先于 PATH),
//     记进系统日志,logger.log 就没有了;
//   - 启动脚本(S99godusevpn)里的程序名、rc.func、标记、PATH 都换到这里。
type entwareRig struct {
	t                                     *testing.T
	busybox                               bool
	name, dir, bin, fake, script, stopped string
	env                                   []string
}

func newEntwareRig(t *testing.T, prefix string, busybox bool) *entwareRig {
	t.Helper()
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
	dir := t.TempDir()
	r := &entwareRig{t: t, busybox: busybox, name: prefix + hex.EncodeToString(rnd[:]), dir: dir, bin: filepath.Join(dir, "bin"),
		script: filepath.Join(dir, "S99godusevpn"), stopped: filepath.Join(dir, "state", "stopped")}
	if len(r.name) > 15 {
		t.Fatalf("冒充的程序名 %q 超过 15 个字符,pidof 认不全", r.name)
	}
	if err := os.MkdirAll(r.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r.fake = filepath.Join(r.bin, r.name)
	copyFile(t, self, r.fake)
	r.write("rc.func", "printf '%s\\n' \"$1\" >> "+filepath.Join(dir, "rc.log")+"\n"+fakeRCFunc, 0o644)
	r.write("bin/cru", "#!/bin/sh\necho \"$*\" >> "+filepath.Join(dir, "cru.log")+"\n", 0o755)
	r.write("bin/logger", "#!/bin/sh\necho \"$*\" >> "+filepath.Join(dir, "logger.log")+"\n", 0o755)
	r.env = append(os.Environ(), fakeEnv+"="+dir, "FAKE_SCRIPT="+r.script)
	if busybox {
		bb, err := exec.LookPath("busybox")
		if err != nil {
			t.Skip("没有 busybox")
		}
		for _, a := range busyboxApplets {
			if err := os.Symlink(bb, filepath.Join(r.bin, a)); err != nil {
				t.Fatal(err)
			}
		}
		r.env = append(r.env, "PATH="+r.bin+":"+os.Getenv("PATH")) // 调用者跑的 sh 也用 BusyBox 的(后写的同名变量优先)
	}
	t.Cleanup(func() { // 冒充的守护进程一个都不留(只杀名字对得上、还活着的:进程号可能早被别的进程用了)
		for _, pid := range r.alive() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	r.render(true)
	return r
}

func (r *entwareRig) write(rel, s string, mode os.FileMode) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, rel), []byte(s), mode); err != nil {
		r.t.Fatal(err)
	}
}

// render 把启动脚本的当前模板(开机自启开 / 关)写成替身环境里的 S99godusevpn。
func (r *entwareRig) render(enabled bool) {
	r.t.Helper()
	s, _ := entwareScriptLike(map[bool]string{true: "ENABLED=yes", false: "ENABLED=no"}[enabled])
	r.write("S99godusevpn", r.localize(s), 0o755)
}

// localize 脚本里的程序名、rc.func、标记、拉起用的锁、PATH 换到替身环境。
func (r *entwareRig) localize(s string) string {
	r.t.Helper()
	orig := s
	s = strings.Replace(s, "PROCS=godusevpn", "PROCS="+r.name, 1)
	s = strings.Replace(s, "/opt/etc/init.d/rc.func", filepath.Join(r.dir, "rc.func"), 1)
	s = strings.Replace(s, "/opt/var/lib/godusevpn/stopped", r.stopped, 1)
	s = strings.Replace(s, "/opt/var/run/godusevpn.lock", r.lockDir(), 1)
	s = strings.Replace(s, "PATH=/opt/bin:", "PATH="+r.bin+":/opt/bin:", 1)
	if s == orig || strings.Count(s, r.dir) != 4 || !strings.Contains(s, "PROCS="+r.name+"\n") {
		r.t.Fatalf("脚本模板变了,测试里的替换要跟上:\n%s", s)
	}
	return s
}

// lockDir 替身环境里拉起用的锁目录(脚本里是 /opt/var/run/godusevpn.lock;上一级目录一开始不在,脚本自己建)。
func (r *entwareRig) lockDir() string { return filepath.Join(r.dir, "run", "godusevpn.lock") }

// locked 锁目录在不在。
func (r *entwareRig) locked() bool { _, err := os.Stat(r.lockDir()); return err == nil }

// legacy 以前的做法:起停全交给 rc.func(和改之前的脚本落到 rc.func 的那一段一样),返回脚本路径。
func (r *entwareRig) legacy() string {
	r.write("legacy", "ENABLED=yes\nPROCS="+r.name+"\nARGS=run\n. "+filepath.Join(r.dir, "rc.func")+"\n", 0o644)
	return filepath.Join(r.dir, "legacy")
}

func (r *entwareRig) envWith(kv ...string) []string {
	return append(append([]string(nil), r.env...), kv...)
}

// caller 像 godusevpn <sub> 那样(同一个名字,命令行第二项不是 run)调启动脚本的 action;env 追加的环境变量(后写的优先)。
// 返回退出码、输出,以及调用者是不是活着跑完了 —— 被脚本杀了就没有结果。
func (r *entwareRig) caller(sub, action string, env ...string) (code int, out string, alive bool) {
	r.t.Helper()
	code, out, alive, err := r.callerRun(sub, action, env...)
	if err != nil {
		r.t.Fatal(err)
	}
	return code, out, alive
}

// callerRun 同 caller,出错时返回错误而不是让测试失败(别的 goroutine 里用)。
func (r *entwareRig) callerRun(sub, action string, env ...string) (code int, out string, alive bool, err error) {
	_ = os.Remove(filepath.Join(r.dir, "caller.out"))
	c := exec.Command(r.fake, sub)
	c.Env = r.envWith(append([]string{"FAKE_ACTION=" + action}, env...)...)
	if err := c.Start(); err != nil {
		return 0, "", false, err
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case <-done:
	case <-time.After(40 * time.Second):
		_ = c.Process.Kill()
		return 0, "", false, fmt.Errorf("调用者跑 %s 超时", action)
	}
	b, err := os.ReadFile(filepath.Join(r.dir, "caller.out"))
	if err != nil {
		return 0, "", false, nil // 被杀了,结果没写出来
	}
	first, rest, _ := strings.Cut(string(b), "\n")
	code, _ = strconv.Atoi(first)
	return code, rest, true, nil
}

// run 测试进程自己跑一遍脚本:调用者不叫这个名字,和 cru 的看门狗、开机时 rc.unslung 调它一样。
// args 是动作和后面的参数(rc.unslung 调的时候第二项是它自己的 CALLER)。
func (r *entwareRig) run(args ...string) (string, error) {
	sh := "sh"
	if r.busybox {
		sh = filepath.Join(r.bin, "sh")
	}
	c := exec.Command(sh, append([]string{r.script}, args...)...)
	c.Env = r.env
	b, err := c.CombinedOutput()
	return string(b), err
}

// exitCode 跑完的退出码:没出错是 0,出错取 ExitError 的;根本没跑起来给 -1。
func exitCode(err error) int {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitCode()
	}
	return -1
}

// startDaemon 测试自己起一个守护进程("名字 run"),等它登记好;返回进程号和等它结束的通道(收尸在这里做)。
func (r *entwareRig) startDaemon() (int, chan error) {
	r.t.Helper()
	c := exec.Command(r.fake, "run")
	c.Env = r.env
	if err := c.Start(); err != nil {
		r.t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	pid := c.Process.Pid
	waitFor(r.t, "守护进程登记进程号", func() bool { return containsInt(r.alive(), pid) })
	return pid, done
}

// idle 起一个不相干的同名进程("名字 idle",像开着的 godusevpn logs -f),返回进程号;测试结束时杀掉、收尸。
func (r *entwareRig) idle() int {
	r.t.Helper()
	c := exec.Command(r.fake, "idle")
	c.Env = r.env
	if err := c.Start(); err != nil {
		r.t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	r.t.Cleanup(func() { _ = c.Process.Kill(); <-done })
	pid := c.Process.Pid
	waitFor(r.t, "同名进程跑起来", func() bool { return fakeAlive(pid, r.name) })
	return pid
}

// alive 登记过、现在还活着的冒充守护进程(按进程号排好)。
func (r *entwareRig) alive() []int {
	var out []int
	for _, pid := range runPIDs(r.dir) {
		if fakeAlive(pid, r.name) {
			out = append(out, pid)
		}
	}
	sort.Ints(out)
	return out
}

// settled 等一小会儿再看还活着的守护进程:确认"没有多起一个"用,刚拉起的要过一会儿才登记。
func (r *entwareRig) settled() []int {
	time.Sleep(500 * time.Millisecond)
	return r.alive()
}

func (r *entwareRig) log(name string) string {
	b, _ := os.ReadFile(filepath.Join(r.dir, name))
	return strings.TrimSpace(string(b))
}

func (r *entwareRig) clearLogs() {
	for _, n := range []string{"rc.log", "cru.log", "logger.log"} {
		_ = os.Remove(filepath.Join(r.dir, n))
	}
}

func (r *entwareRig) marked() bool { _, err := os.Stat(r.stopped); return err == nil }

// mark 留下"用户停了"的标记(和 stop 留的一样)。
func (r *entwareRig) mark() {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(r.stopped), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(r.stopped, nil, 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// cruLine 脚本挂的看门狗定时(替身环境里程序名换过,定时编号跟着换)。
func (r *entwareRig) cruLine() string {
	return "a " + r.name + " * * * * * /opt/etc/init.d/S99godusevpn watchdog"
}

// waitSignaled 测试自己起的守护进程应被信号 sig 结束。
func waitSignaled(t *testing.T, done chan error, sig syscall.Signal) {
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

// 梅林(Entware)上 godusevpn uninstall / stop 调启动脚本停服务,不能把正在调它的自己也杀了:
// 以前脚本的 stop 交给 rc.func,按名字 killall godusevpn,调用者也叫这个名字,卸载做到一半就没了。
// 真起几个同名进程跑一遍脚本:stop 只停 "名字 run" 那个守护进程、调用者活着拿到退出码 0;没在跑时也一样;kill 用 KILL;
// restart 停掉旧的、拉起新的;最后用以前的做法(交给 rc.func)对照,确认替身 rc.func 确实会连调用者一起杀。
func TestEntwareStopSparesCaller(t *testing.T) {
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gvstop", busybox)

		// 1. godusevpn uninstall / stop 调脚本 stop:守护进程(TERM)停了,调用者活着、退出码 0,留下"用户停了"的标记
		_, done := r.startDaemon()
		code, out, alive := r.caller("uninstall", "stop")
		if !alive || code != 0 {
			t.Fatalf("调脚本 stop 的调用者应活着拿到退出码 0: alive=%v code=%d\n%s", alive, code, out)
		}
		waitSignaled(t, done, syscall.SIGTERM)
		if !r.marked() {
			t.Fatal("stop 要留下标记,看门狗才不拉")
		}

		// 2. 守护进程本来就没在跑(比如升级失败后来卸载):同样不碰调用者,直接成功
		if code, out, alive := r.caller("uninstall", "stop"); !alive || code != 0 {
			t.Fatalf("没在跑时 stop 也不该杀调用者: alive=%v code=%d\n%s", alive, code, out)
		}

		// 3. kill:用 KILL
		_, done = r.startDaemon()
		if code, out, alive := r.caller("uninstall", "kill"); !alive || code != 0 {
			t.Fatalf("kill 也不该杀调用者: alive=%v code=%d\n%s", alive, code, out)
		}
		waitSignaled(t, done, syscall.SIGKILL)

		// 4. restart(面板升级经 Restart 走这条):停掉旧的、去掉标记、拉起新的(新的不是本测试的子进程,按登记的进程号找)
		old, done := r.startDaemon()
		rsOut, err := r.run("restart")
		if err != nil {
			t.Fatalf("restart: %v\n%s", err, rsOut)
		}
		waitSignaled(t, done, syscall.SIGTERM)
		if r.marked() {
			t.Fatal("restart 要去掉标记")
		}
		waitFor(t, "restart 拉起一个新的守护进程", func() bool { a := r.alive(); return len(a) == 1 && a[0] != old })
		fresh := r.alive()

		// 5. 对照:以前的做法(stop 交给 rc.func,按名字 killall)会把调用者一起杀掉 —— 替身 rc.func 和真的一样会出这个事
		if _, _, alive := r.caller("uninstall", "stop", "FAKE_SCRIPT="+r.legacy()); alive {
			t.Fatal("对照失败:交给 rc.func 的 stop 应该连调用者一起杀掉(替身和真的 rc.func 不一致?)")
		}
		waitFor(t, "对照里的守护进程也被 killall 掉", func() bool { return !fakeAlive(fresh[0], r.name) })
	})
}

// 梅林(Entware)上 godusevpn install / start 调启动脚本起服务,不能把正在调它的自己当成"已经在跑":以前脚本的 start
// 交给 rc.func,它按名字 pidof godusevpn,调用者也叫这个名字 —— 装完服务没起来,梅林要等看门狗一分钟后才拉,
// 没有 cru 的 Entware 要等重启。真起几个同名进程跑一遍脚本:install 调 start 返回时守护进程已经在跑;服务在跑时
// 再 start 什么都不动;程序不在时等满 10 秒报失败;最后用以前的做法(交给 rc.func)对照,确认替身 rc.func 确实会把调用者
// 当成服务、一个都不拉。程序起服务调的是脚本的 manual-start(startAction),开机自启的开关见 TestEntwareAutostartOnlyGatesBoot。
func TestEntwareStartFromCaller(t *testing.T) {
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gvstrt", busybox)

		// 1. godusevpn install 调 start(之前用户 stop 过,留着标记):返回时守护进程已经起来了,只有一个;
		//    标记去掉、看门狗定时挂上、和 rc.func 一样往系统日志记一笔;不经 rc.func
		r.mark()
		code, out, alive := r.caller("install", startAction(initEntware))
		if !alive || code != 0 {
			t.Fatalf("调脚本 start 的调用者应活着拿到退出码 0: alive=%v code=%d\n%s", alive, code, out)
		}
		if ds := entwareDaemonsByScan(r.name); len(ds) != 1 {
			t.Fatalf("start 返回时守护进程就应该在跑了(\"名字 run\" 的恰好一个),得 %v\n%s", ds, out)
		}
		waitFor(t, "守护进程登记进程号", func() bool { return len(r.alive()) == 1 })
		first := r.alive()
		if r.marked() {
			t.Fatal("start 要去掉标记")
		}
		if got := r.log("cru.log"); got != r.cruLine() {
			t.Fatalf("start 要挂上看门狗定时: %q", got)
		}
		if got := r.log("logger.log"); !busybox && got != "Started "+r.name+" from "+startAction(initEntware)+"." { // BusyBox 的 sh 直接用自己的 logger,记进系统日志
			t.Fatalf("起来了要和 rc.func 一样记一笔: %q", got)
		}
		if got := r.log("rc.log"); got != "" {
			t.Fatalf("start 不该交给 rc.func(它按名字认进程),rc.func 收到 %q", got)
		}

		// 2. 服务在跑时再 start(godusevpn start、重装再 install):什么都不起,照样 0
		r.clearLogs()
		code, out, alive = r.caller("start", startAction(initEntware))
		if !alive || code != 0 || !strings.Contains(out, "already running") {
			t.Fatalf("服务在跑时 start 应直接成功: alive=%v code=%d\n%s", alive, code, out)
		}
		if got := r.settled(); !reflect.DeepEqual(got, first) || len(entwareDaemonsByScan(r.name)) != 1 {
			t.Fatalf("服务在跑时 start 不该再起一个: %v(原来 %v)", got, first)
		}

		// 3. 停掉(最后的对照组要从没在跑开始)
		if out, err := r.run("stop"); err != nil {
			t.Fatalf("stop: %v\n%s", err, out)
		}
		waitFor(t, "守护进程停掉", func() bool { return len(r.alive()) == 0 })

		// 4. 程序不在(链接丢了之类):等满 10 秒报失败,退出码 255,也记一笔;看门狗下一分钟再试
		r.clearLogs()
		missing := filepath.Join(r.dir, "S99missing")
		r.write("S99missing", strings.Replace(r.localize(entwareScript), "PROCS="+r.name+"\n", "PROCS="+r.name+"x\n", 1), 0o755)
		sh := "sh"
		if busybox {
			sh = filepath.Join(r.bin, "sh")
		}
		c := exec.Command(sh, missing, "start")
		c.Env = r.env
		began := time.Now()
		b, err := c.CombinedOutput()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 255 || !strings.Contains(string(b), "failed to start") || time.Since(began) < 9*time.Second {
			t.Fatalf("程序不在时应等满 10 秒报失败(255): %v %v\n%s", err, time.Since(began), b)
		}
		if got := r.log("logger.log"); !busybox && got != "Failed to start "+r.name+"x from start." {
			t.Fatalf("没起来也要记一笔: %q", got)
		}
		if got := r.log("cru.log"); !strings.Contains(got, " watchdog") {
			t.Fatalf("没起来也要挂上看门狗定时,每分钟再试: %q", got)
		}
		if r.locked() {
			t.Fatal("没起来(255)也要放掉拉起用的锁")
		}

		// 5. 对照:以前的做法(start 交给 rc.func,按名字 pidof)把调用者当成服务已经在跑,一个都不拉
		code, out, alive = r.caller("install", "start", "FAKE_SCRIPT="+r.legacy())
		if !alive || code != 0 || !strings.Contains(out, "already running") {
			t.Fatalf("对照失败:交给 rc.func 的 start 应把调用者当成已经在跑(替身和真的 rc.func 不一致?) alive=%v code=%d\n%s", alive, code, out)
		}
		time.Sleep(time.Second)
		if got := r.alive(); len(got) != 0 {
			t.Fatalf("对照失败:交给 rc.func 的 start 不该拉起守护进程: %v", got)
		}
	})
}

// 梅林(Entware)的看门狗:真跑一遍脚本。起服务时挂上定时、去掉"用户停了"的标记;stop 留标记;定时看到标记、
// 或服务在跑就不拉,否则(崩了)拉起来 —— 开机自启关着也拉(在跑就是用户起的,和 systemd / procd 一样)。
// 起停都不交给 rc.func(它按名字认进程,见上面两条)。
func TestEntwareWatchdog(t *testing.T) {
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gvwdog", busybox)
		run := func(action string) {
			t.Helper()
			r.clearLogs()
			if out, err := r.run(action); err != nil {
				t.Fatalf("%s: %v\n%s", action, err, out)
			}
			if got := r.log("rc.log"); got != "" {
				t.Fatalf("%s 不该交给 rc.func(它按名字认进程),rc.func 收到 %q", action, got)
			}
		}

		run("stop")
		if !r.marked() {
			t.Fatal("stop 要留下标记,看门狗才不拉")
		}
		run("watchdog")
		if got := r.settled(); len(got) != 0 {
			t.Fatalf("用户停了的服务定时不该拉起来: %v", got)
		}

		run("start")
		if r.marked() || r.log("cru.log") != r.cruLine() {
			t.Fatalf("start 要去掉标记、挂上定时: 标记=%v cru=%q", r.marked(), r.log("cru.log"))
		}
		if want := strings.Join(entwareWatchdog, " "); strings.Replace(r.log("cru.log"), r.name, name, 1) != want {
			t.Fatalf("守护进程补挂的定时要和脚本里的一致: %q ≠ %q", want, r.log("cru.log"))
		}
		waitFor(t, "start 拉起守护进程", func() bool { return len(r.alive()) == 1 })
		first := r.alive()
		run("watchdog")
		if got := r.settled(); !reflect.DeepEqual(got, first) {
			t.Fatalf("服务在跑时定时不该再拉: %v(原来 %v)", got, first)
		}

		// 崩了(不是用户停的):定时拉起来
		_ = syscall.Kill(first[0], syscall.SIGKILL)
		waitFor(t, "守护进程没了", func() bool { return len(r.alive()) == 0 })
		run("watchdog")
		waitFor(t, "看门狗拉起新的守护进程", func() bool { return len(r.alive()) == 1 })
		if got := r.log("logger.log"); !busybox && got != "Started "+r.name+" from watchdog." {
			t.Fatalf("看门狗拉起来也记一笔: %q", got)
		}

		// stop 之后 restart(面板升级走这条):去掉标记、拉起来
		run("stop")
		waitFor(t, "守护进程停掉", func() bool { return len(r.alive()) == 0 })
		run("restart")
		if r.marked() {
			t.Fatal("restart 要去掉标记")
		}
		waitFor(t, "restart 拉起守护进程", func() bool { return len(r.alive()) == 1 })

		// 开机自启关着(服务在跑时关的):崩了定时照样拉 —— 那个开关只管开机,systemd / procd 关了自启也照样重拉
		cur := r.alive()
		r.render(false)
		_ = syscall.Kill(cur[0], syscall.SIGKILL)
		waitFor(t, "守护进程没了", func() bool { return len(r.alive()) == 0 })
		run("watchdog")
		waitFor(t, "开机自启关着时看门狗也拉起新的守护进程", func() bool { return len(r.alive()) == 1 })
	})
}

// 梅林(Entware)的开机自启开关只管开机,和 systemd / procd 一样:rc.unslung 开机时调脚本的 start,自启关着就照 rc.func
// 返回 8、不起,标记和看门狗定时也不动(开机没起,一分钟后看门狗也不会替它起);godusevpn start / install 调的是
// manual-start,不看开关;面板升级后的重启(脚本的 restart)照样重启正在跑的,没在跑的不起;看门狗也不看开关,手动起的
// 崩了照样拉。以前程序调的也是 start:自启关着时手动起不来(返回 8),面板升级的 restart 停了旧的、新的不起,服务一直停着。
func TestEntwareAutostartOnlyGatesBoot(t *testing.T) {
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gvboot", busybox)
		r.render(false)
		r.mark() // 关机时 rc.unslung stop 留下的

		// 1. 开机:rc.unslung start(第二项是它自己的 CALLER)。不起、照 rc.func 返回 8;标记还在、不挂看门狗定时、不交给 rc.func
		out, err := r.run("start", "/jffs/scripts/post-mount")
		if code := exitCode(err); code != 8 {
			t.Fatalf("开机自启关着时 rc.unslung start 应照 rc.func 返回 8,得 %d\n%s", code, out)
		}
		if got := r.settled(); len(got) != 0 {
			t.Fatalf("开机自启关着时开机不该起: %v", got)
		}
		if !r.marked() || r.log("cru.log") != "" || r.log("rc.log") != "" {
			t.Errorf("开机没起就什么都不动(不然看门狗一分钟后替它起):标记=%v cru=%q rc.func=%q", r.marked(), r.log("cru.log"), r.log("rc.log"))
		}

		// 2. rc.unslung restart(没在跑):同样不起、返回 8,什么都不动
		r.clearLogs()
		out, err = r.run("restart")
		if code := exitCode(err); code != 8 || len(r.settled()) != 0 {
			t.Fatalf("自启关着、没在跑时 restart 不该把它起来: code=%d 守护进程=%v\n%s", code, r.alive(), out)
		}
		if !r.marked() || r.log("cru.log") != "" {
			t.Errorf("restart 没起就什么都不动:标记=%v cru=%q", r.marked(), r.log("cru.log"))
		}

		// 3. godusevpn start(程序调的动作,同名的调用者):照样起来、只有一个;去掉标记、挂上看门狗定时
		r.clearLogs()
		code, out, alive := r.caller("start", startAction(initEntware))
		if !alive || code != 0 {
			t.Fatalf("开机自启关着时 godusevpn start 也应起来(开关只管开机): alive=%v code=%d\n%s", alive, code, out)
		}
		if ds := entwareDaemonsByScan(r.name); len(ds) != 1 {
			t.Fatalf("start 返回时守护进程就应该在跑了(恰好一个),得 %v\n%s", ds, out)
		}
		waitFor(t, "守护进程登记进程号", func() bool { return len(r.alive()) == 1 })
		if r.marked() || r.log("cru.log") != r.cruLine() {
			t.Fatalf("手动起了要去掉标记、挂上看门狗定时: 标记=%v cru=%q", r.marked(), r.log("cru.log"))
		}
		first := r.alive()

		// 4. 崩了:看门狗照样拉起来(和 systemd 的 Restart=on-failure、procd 的 respawn 一样不看开机自启)
		_ = syscall.Kill(first[0], syscall.SIGKILL)
		waitFor(t, "守护进程没了", func() bool { return len(r.alive()) == 0 })
		if out, err := r.run("watchdog"); err != nil {
			t.Fatalf("watchdog: %v\n%s", err, out)
		}
		waitFor(t, "看门狗拉起新的守护进程", func() bool { return len(r.alive()) == 1 })
		second := r.alive()

		// 5. 面板升级后的重启(Restart 交给脚本的 restart;rc.unslung restart 也走这条):停旧的、起新的,只有一个
		out, err = r.run("restart")
		if err != nil {
			t.Fatalf("开机自启关着时 restart 也应重启正在跑的: %v\n%s", err, out)
		}
		waitFor(t, "restart 停了旧的、起了新的", func() bool { a := r.alive(); return len(a) == 1 && a[0] != second[0] })
		if got := r.settled(); len(got) != 1 || len(entwareDaemonsByScan(r.name)) != 1 {
			t.Fatalf("restart 之后应只有一个守护进程: %v", got)
		}

		// 6. godusevpn stop 之后:开机的 start 照旧不起;再 godusevpn start 又起来
		if code, out, alive := r.caller("stop", "stop"); !alive || code != 0 {
			t.Fatalf("stop: alive=%v code=%d\n%s", alive, code, out)
		}
		waitFor(t, "守护进程停掉", func() bool { return len(r.alive()) == 0 })
		if out, err := r.run("start"); exitCode(err) != 8 || len(r.settled()) != 0 {
			t.Fatalf("开机自启关着时 start 照旧不起: %v %v\n%s", err, r.alive(), out)
		}
		if code, out, alive := r.caller("start", startAction(initEntware)); !alive || code != 0 || len(entwareDaemonsByScan(r.name)) != 1 {
			t.Fatalf("godusevpn start 又能起来: alive=%v code=%d\n%s", alive, code, out)
		}
	})
}

// 梅林(Entware)上用户手动跑 rc.unslung reconfigure:以前脚本的 reconfigure 交给 rc.func,它按名字 killall -HUP godusevpn ——
// 守护进程不接 SIGHUP、收到就退出(调用者、别的 godusevpn 命令也一样),服务就这么停了,要等看门狗一分钟后才拉。
// 现在脚本自己处理:重启守护进程(和 restart 一样只按进程号停它),调用者和别的同名进程一个不碰;没在跑就不动
// (rc.func 的 reconfigure 也只给在跑的进程发信号,从不拉起)。最后用以前的做法(交给 rc.func)对照,确认替身 rc.func
// 确实会用 HUP 把守护进程、调用者和别的同名进程一起打掉。
func TestEntwareReconfigureRestartsDaemon(t *testing.T) {
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gvrcfg", busybox)
		bystander := r.idle() // 不相干的同名进程

		// 1. 守护进程在跑,同名的调用者跑 reconfigure:旧的被 TERM 停掉、起了新的,只有一个;调用者活着拿到 0;
		//    别的同名进程还在;不经 rc.func
		old, done := r.startDaemon()
		code, out, alive := r.caller("reconfigure", "reconfigure")
		if !alive || code != 0 {
			t.Errorf("跑 reconfigure 的调用者应活着拿到退出码 0: alive=%v code=%d\n%s", alive, code, out)
		}
		if !fakeAlive(bystander, r.name) {
			t.Error("reconfigure 不该碰别的同名进程")
		}
		waitSignaled(t, done, syscall.SIGTERM) // 不是 HUP:守护进程不接 SIGHUP,收到就退出
		waitFor(t, "reconfigure 起了新的守护进程", func() bool { a := r.alive(); return len(a) == 1 && a[0] != old })
		if got := r.settled(); len(got) != 1 || len(entwareDaemonsByScan(r.name)) != 1 {
			t.Fatalf("reconfigure 之后应只有一个守护进程: %v", got)
		}
		if got := r.log("rc.log"); got != "" {
			t.Fatalf("reconfigure 不该交给 rc.func(它按名字 killall -HUP),rc.func 收到 %q", got)
		}

		// 2. rc.unslung reconfigure(调用者不同名),开机自启关着也一样:在跑的就重启
		r.render(false)
		cur := r.alive()
		if out, err := r.run("reconfigure"); err != nil {
			t.Fatalf("reconfigure: %v\n%s", err, out)
		}
		waitFor(t, "reconfigure 又起了新的守护进程", func() bool { a := r.alive(); return len(a) == 1 && a[0] != cur[0] })
		r.render(true)

		// 3. 没在跑(用户停了):不起,返回 0,标记留着
		if out, err := r.run("stop"); err != nil {
			t.Fatalf("stop: %v\n%s", err, out)
		}
		waitFor(t, "守护进程停掉", func() bool { return len(r.alive()) == 0 })
		out, err := r.run("reconfigure")
		if code := exitCode(err); code != 0 || len(r.settled()) != 0 || !r.marked() {
			t.Fatalf("没在跑时 reconfigure 不该把它起来: code=%d 守护进程=%v 标记=%v\n%s", code, r.alive(), r.marked(), out)
		}
		if !fakeAlive(bystander, r.name) {
			t.Fatal("reconfigure 不该碰别的同名进程")
		}

		// 4. 对照:以前的做法(reconfigure 交给 rc.func,按名字 killall -HUP)把守护进程、调用者、别的同名进程全打掉
		_, done = r.startDaemon()
		if _, _, alive := r.caller("reconfigure", "reconfigure", "FAKE_SCRIPT="+r.legacy()); alive {
			t.Fatal("对照失败:交给 rc.func 的 reconfigure 应该连调用者一起用 HUP 打掉(替身和真的 rc.func 不一致?)")
		}
		waitSignaled(t, done, syscall.SIGHUP)
		waitFor(t, "对照里别的同名进程也被 HUP 打掉", func() bool { return !fakeAlive(bystander, r.name) })
	})
}

// 梅林(Entware):守护进程刚崩,用户手动起服务恰好撞上看门狗整分钟的那一下 —— 两边都查到没在跑,就各拉起一个
// (两个守护进程抢同一个控制口、同一张 TUN 和防火墙规则)。现在"查有没有在跑 → 没有就拉起 → 等它出现"那一段有锁
// (mkdir 建的锁目录),同一时间只有一个脚本在拉,另一边拿到锁时守护进程已经在了,只说 already running。
// 真并发两路:一路像 cru 那样跑 watchdog,一路是同名的调用者跑 godusevpn start 的动作,同时放出去,重复多轮,
// 每轮之后都只有一个守护进程、只有一边真的拉起(记了一笔 Started)、锁放掉了。"慢起"那几轮让守护进程拉起之后
// 0.3 秒才出现(路由器从 U 盘加载几十 MB 的程序就是这样),撞车的窗口放大:没有锁时这几轮都会起两个。
func TestEntwareStartRacesWatchdog(t *testing.T) {
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gvrace", busybox)
		r.write("slowexec", "#!/bin/sh\nsleep 0.3\nexec \"$@\"\n", 0o755)
		slow := strings.Replace(r.localize(entwareScript), `PREARGS=""`, `PREARGS="`+filepath.Join(r.dir, "slowexec")+`"`, 1)
		if !strings.Contains(slow, "slowexec") {
			t.Fatal("脚本模板变了:没有 PREARGS=\"\"")
		}
		for _, v := range []struct{ name, script string }{{"照常", r.localize(entwareScript)}, {"慢起", slow}} {
			r.write("S99godusevpn", v.script, 0o755)
			for i := 0; i < 5; i++ {
				// 守护进程刚崩:没在跑、没有"用户停了"的标记
				for _, pid := range r.alive() {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
				waitFor(t, "守护进程没了", func() bool { return len(r.alive()) == 0 })
				_ = os.Remove(r.stopped)
				r.clearLogs()

				var (
					wg          sync.WaitGroup
					wOut, cOut  string
					wErr, cErr  error
					code        int
					callerAlive bool
				)
				gate := make(chan struct{})
				wg.Add(2)
				go func() { // cru 的看门狗;错开 0~9 毫秒,把两边查"在不在跑"的先后来回挪
					defer wg.Done()
					<-gate
					time.Sleep(time.Duration(i%4) * 3 * time.Millisecond)
					wOut, wErr = r.run("watchdog")
				}()
				go func() { // 用户的 godusevpn start
					defer wg.Done()
					<-gate
					code, cOut, callerAlive, cErr = r.callerRun("start", startAction(initEntware))
				}()
				close(gate)
				wg.Wait()
				if wErr != nil || cErr != nil || !callerAlive || code != 0 {
					t.Fatalf("%s 第 %d 轮:看门狗 %v / 手动 start alive=%v code=%d %v\n看门狗: %s\n手动 start: %s", v.name, i, wErr, callerAlive, code, cErr, wOut, cOut)
				}
				if ds := entwareDaemonsByScan(r.name); len(ds) != 1 {
					t.Fatalf("%s 第 %d 轮:看门狗和手动 start 同时跑,应只有一个守护进程,得 %v\n看门狗: %s\n手动 start: %s", v.name, i, ds, wOut, cOut)
				}
				if got := r.settled(); len(got) != 1 {
					t.Fatalf("%s 第 %d 轮:过一会儿应还是只有一个守护进程,得 %v", v.name, i, got)
				}
				if r.locked() {
					t.Fatalf("%s 第 %d 轮:两边都跑完了,锁应已放掉", v.name, i)
				}
				if got := r.log("logger.log"); !busybox && strings.Count(got, "Started ") != 1 { // BusyBox 的 logger 记进系统日志
					t.Fatalf("%s 第 %d 轮:应只有一边真的拉起(记一笔 Started),得 %q", v.name, i, got)
				}
			}
		}
	})
}

// uptimeSeconds 开机到现在的秒数(/proc/uptime 的整数部分,和脚本记进锁里的一样)。
func uptimeSeconds(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		t.Fatalf("/proc/uptime: %q", b)
	}
	n, err := strconv.Atoi(strings.SplitN(f[0], ".", 2)[0])
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// 拉起那一段的锁:mkdir 建的锁目录,里面 owner 写持有者的进程号和开机秒数。别人拿着(进程还在、没超过 30 秒)就等着,
// 放掉了接着起;持有者已经不在、锁拿了超过 30 秒、或者是重启前留下的(开机秒数比现在还大),当成没放掉的旧锁清掉,
// 不用干等;记录没写上的(持有者刚建完目录就没了),等满时限也清掉。退出时都放锁:已经在跑、起来了、被 TERM 打断
// (没起来报 255 的那条见 TestEntwareStartFromCaller)。
func TestEntwareLaunchLock(t *testing.T) {
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gvlock", busybox)
		sh := "sh"
		if busybox {
			sh = filepath.Join(r.bin, "sh")
		}
		hold := func(owner string) { // 替别人拿着锁
			t.Helper()
			if err := os.MkdirAll(r.lockDir(), 0o755); err != nil {
				t.Fatal(err)
			}
			if owner != "" {
				if err := os.WriteFile(filepath.Join(r.lockDir(), "owner"), []byte(owner+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		stopAll := func() {
			t.Helper()
			if out, err := r.run("stop"); err != nil {
				t.Fatalf("stop: %v\n%s", err, out)
			}
			waitFor(t, "守护进程停掉", func() bool { return len(r.alive()) == 0 })
		}
		// startOK 起服务,应在 within 之内起来、只有一个,锁放掉
		startOK := func(what string, within time.Duration) {
			t.Helper()
			began := time.Now()
			out, err := r.run(startAction(initEntware))
			if err != nil || time.Since(began) > within {
				t.Fatalf("%s:应在 %v 内起来,用了 %v: %v\n%s", what, within, time.Since(began), err, out)
			}
			if ds := entwareDaemonsByScan(r.name); len(ds) != 1 {
				t.Fatalf("%s:应起了一个守护进程,得 %v\n%s", what, ds, out)
			}
			if r.locked() {
				t.Fatalf("%s:起完要放锁", what)
			}
		}
		other := exec.Command("sleep", "120") // 别的活着的进程(拿着锁的"别人")
		if err := other.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
		holder := other.Process.Pid

		// 1. 别人拿着(进程活着、刚拿):等着不起,也不动别人的锁;放掉之后接着起来
		hold(fmt.Sprintf("%d %d", holder, uptimeSeconds(t)))
		res := make(chan error, 1)
		go func() { _, err := r.run(startAction(initEntware)); res <- err }()
		time.Sleep(2500 * time.Millisecond)
		select {
		case err := <-res:
			t.Fatalf("锁被别人拿着时应等着,却返回了: %v(守护进程 %v)", err, r.alive())
		default:
		}
		if got := r.alive(); len(got) != 0 || !r.locked() {
			t.Fatalf("锁被别人拿着时不该拉起、不该动别人的锁: 守护进程=%v 锁在=%v", got, r.locked())
		}
		_ = os.RemoveAll(r.lockDir()) // 别人放锁
		select {
		case err := <-res:
			if err != nil {
				t.Fatalf("放锁之后应接着起来: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("放锁之后还在等")
		}
		waitFor(t, "放锁之后起来了", func() bool { return len(r.alive()) == 1 })
		if r.locked() {
			t.Fatal("起完要放锁")
		}

		// 2. 已经在跑:already running,也放锁
		if out, err := r.run(startAction(initEntware)); err != nil || !strings.Contains(out, "already running") || r.locked() {
			t.Fatalf("已经在跑时应直接成功并放锁: %v 锁在=%v\n%s", err, r.locked(), out)
		}
		stopAll()

		// 3. 持有者已经不在了:当成旧锁清掉,马上起
		dead := exec.Command("true")
		if err := dead.Run(); err != nil {
			t.Fatal(err)
		}
		hold(fmt.Sprintf("%d %d", dead.Process.Pid, uptimeSeconds(t)))
		startOK("持有者已经不在的旧锁", 5*time.Second)
		stopAll()

		// 4. 持有者活着(进程号给了别的程序之类),但锁拿了超过 30 秒:清掉,不用干等
		if up := uptimeSeconds(t); up > 40 { // 刚开机的机器上没法往前倒 31 秒
			hold(fmt.Sprintf("%d %d", holder, up-31))
			startOK("拿了超过 30 秒的旧锁", 5*time.Second)
			stopAll()
		}

		// 5. 重启前留下的(锁在 /opt 上,重启不丢):开机秒数比现在还大
		hold(fmt.Sprintf("%d %d", holder, uptimeSeconds(t)+100000))
		startOK("重启前留下的旧锁", 5*time.Second)
		stopAll()

		// 6. 记录没写上(持有者刚建完目录就没了):等满时限(这里把 30 秒改成 3 秒)再清掉
		r.write("S99godusevpn", strings.Replace(r.localize(entwareScript), "LOCKTTL=30", "LOCKTTL=3", 1), 0o755)
		hold("")
		began := time.Now()
		startOK("没写记录的旧锁", 10*time.Second)
		if d := time.Since(began); d < 2500*time.Millisecond {
			t.Fatalf("没写记录的锁不知道是谁的,要等满时限才清(等了 %v)", d)
		}
		stopAll()
		r.render(true)

		// 7. 拿着锁时被 TERM 打断(程序不在,一直等它出现):先放锁再退出
		missing := filepath.Join(r.dir, "S99missing")
		r.write("S99missing", strings.Replace(r.localize(entwareScript), "PROCS="+r.name+"\n", "PROCS="+r.name+"x\n", 1), 0o755)
		c := exec.Command(sh, missing, startAction(initEntware))
		c.Env = r.env
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "脚本拿到锁", func() bool {
			b, _ := os.ReadFile(filepath.Join(r.lockDir(), "owner"))
			return strings.HasPrefix(string(b), strconv.Itoa(c.Process.Pid)+" ")
		})
		_ = c.Process.Signal(syscall.SIGTERM)
		err := c.Wait()
		if code := exitCode(err); code != 143 || r.locked() {
			t.Fatalf("被 TERM 打断要放锁再退出(143): code=%d 锁在=%v", code, r.locked())
		}
	})
}

// query 像 godusevpn status 那样,由一个同名进程("名字 status",命令行第二项不是 run)问服务在不在跑。
// 返回这个问的进程自己的进程号、现在的判断(entwareDaemons,QueryStatus 用它)认出的守护进程、以前那样只按名字 pidof 的结果。
func (r *entwareRig) query() (self int, daemons, byName []int) {
	r.t.Helper()
	_ = os.Remove(filepath.Join(r.dir, "query.out"))
	c := exec.Command(r.fake, "status")
	c.Env = r.envWith("FAKE_QUERY=" + r.name)
	if out, err := c.CombinedOutput(); err != nil {
		r.t.Fatalf("问状态的同名进程出错: %v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(r.dir, "query.out"))
	if err != nil {
		r.t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) < 3 {
		r.t.Fatalf("query.out 不对: %q", b)
	}
	ints := func(s string) []int {
		var out []int
		for _, f := range strings.Fields(s) {
			n, err := strconv.Atoi(f)
			if err != nil {
				r.t.Fatalf("query.out 不对: %q", b)
			}
			out = append(out, n)
		}
		return out
	}
	self, _ = strconv.Atoi(lines[0])
	return self, ints(lines[1]), ints(lines[2])
}

// 梅林(Entware)上 godusevpn status 问服务在不在跑(QueryStatus):以前按名字 pidof godusevpn,正在问的本进程自己就算
// 一个,服务没在跑也报"在跑"。脚本的 check(rc.unslung check 走这条)以前交给 rc.func,同样只按名字认。
// 现在两处都和起停一样只认守护进程。真起同名进程问一遍:没在跑时答没在跑(对照:只按名字的 pidof 确实把问的那个
// 自己算进去、rc.func 的 check 确实说活着);守护进程在跑时认出的正是它;它退出了、还没收尸(僵尸)也不算。
func TestEntwareStatusFromCaller(t *testing.T) {
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gvstat", busybox)
		check := func(want int) {
			t.Helper()
			r.clearLogs()
			code, out, alive := r.caller("status", "check")
			if !alive || code != want {
				t.Fatalf("同名命令跑脚本的 check 应得 %d: alive=%v code=%d\n%s", want, alive, code, out)
			}
			if got := r.log("rc.log"); got != "" {
				t.Fatalf("check 不该交给 rc.func(它按名字认进程),rc.func 收到 %q", got)
			}
		}

		// 1. 没在跑:答没在跑;脚本的 check 返回 1。对照:只按名字的 pidof 把问的那个自己算进去,交给 rc.func 的 check 说活着
		self, ds, byName := r.query()
		if len(ds) != 0 {
			t.Fatalf("服务没在跑,却认出了守护进程 %v(问的进程 %d)", ds, self)
		}
		if !containsInt(byName, self) {
			t.Fatalf("对照失败:只按名字的 pidof 应把问的进程自己(%d)算进去,得 %v", self, byName)
		}
		check(1)
		if code, out, alive := r.caller("status", "check", "FAKE_SCRIPT="+r.legacy()); !alive || code != 0 {
			t.Fatalf("对照失败:交给 rc.func 的 check 应把调用者当成服务活着: alive=%v code=%d\n%s", alive, code, out)
		}

		// 2. 守护进程在跑:认出的正是它,check 返回 0
		pid, done := r.startDaemon()
		if self, ds, _ := r.query(); !reflect.DeepEqual(ds, []int{pid}) {
			t.Fatalf("应只认出守护进程 %d,得 %v(问的进程 %d)", pid, ds, self)
		}
		check(0)
		_ = syscall.Kill(pid, syscall.SIGTERM)
		waitSignaled(t, done, syscall.SIGTERM)

		// 3. 守护进程退出了、还没收尸(僵尸;BusyBox 的 pidof 照样列出来):不算在跑
		c := exec.Command(r.fake, "run")
		c.Env = r.env
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		zombie := c.Process.Pid
		waitFor(t, "守护进程登记进程号", func() bool { return containsInt(r.alive(), zombie) })
		_ = c.Process.Kill()
		waitFor(t, "守护进程成了僵尸", func() bool { return procState(zombie) == 'Z' })
		if _, ds, _ := r.query(); len(ds) != 0 {
			t.Fatalf("僵尸不算在跑,得 %v", ds)
		}
		check(1)
		_ = c.Wait()
	})
}

// procState /proc/<pid>/stat 里的进程状态(R / S / Z …);读不到给 0。
func procState(pid int) byte {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	st := string(b)
	if r := strings.LastIndexByte(st, ')'); r >= 0 && r+2 < len(st) {
		return st[r+2]
	}
	return 0
}

// entwareDaemonsByScan 测试这边独立数一遍 "名字 run" 的守护进程(扫 /proc,不经 pidof):核对脚本说起来了的时候是不是真起来了。
func entwareDaemonsByScan(name string) []int {
	var out []int
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		args := strings.Split(string(b), "\x00")
		if len(args) > 1 && filepath.Base(args[0]) == name && args[1] == "run" {
			out = append(out, pid)
		}
	}
	return out
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
