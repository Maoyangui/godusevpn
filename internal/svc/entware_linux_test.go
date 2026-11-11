package svc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	sblog "github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"golang.org/x/sys/unix"

	"github.com/Maoyangui/godusevpn/internal/paths"
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
//   - run:守护进程。登记进程号(run.<pid>)后一直挂着,收到 TERM / KILL 就没了。设了 FAKE_REFRESH(一份已装的脚本)
//     就先像真的守护进程启动时那样跑 Refresh 在 Entware 上的那一段(refreshEntware,补挂看门狗定时调 PATH 里的 cru),
//     做完写 refreshed.<pid>;
//   - 设了 FAKE_QUERY:像 godusevpn status 那样问服务在不在跑。把自己的进程号、entwareDaemons(FAKE_QUERY)
//     (QueryStatus 现在的判断)和只按名字 pidof 的结果(以前的判断)写进 query.out,各占一行;
//   - idle:不相干的同名进程(像开着的 godusevpn logs -f),只是挂着,收到信号就没了;
//   - 别的子命令:像 godusevpn install / start / uninstall / stop 那样调启动脚本(sh $FAKE_SCRIPT $FAKE_ACTION),
//     跑完把退出码和输出写进 caller.out —— 自己被脚本杀了就写不出来。
func fakeGodusevpn(dir string, args []string) int {
	if len(args) > 0 && args[0] == "run" {
		pid := strconv.Itoa(os.Getpid())
		if p := os.Getenv("FAKE_REFRESH"); p != "" {
			// 像真的守护进程那样启动时调 Refresh 在 Entware 上的那一段(p 是一份已装的脚本),做完留个记号(内容是返回的错误)
			err := refreshEntware(p)
			_ = os.WriteFile(filepath.Join(dir, "refreshed."+pid), []byte(fmt.Sprint(err)), 0o644)
		}
		var crash chan os.Signal
		if p := os.Getenv("FAKE_STDERR_LOG"); p != "" {
			// 像真的守护进程那样一启动就处理标准错误,再像内核那样往标准错误写连接目标;收到 USR1 就真的崩一次(fatal error)。
			// 崩之前先让垃圾回收和终结器跑几遍:原来的标准错误(fd 2)要是没人引用着,终结器会把它关掉,崩溃输出就丢了。
			// sing-box 的日志包初始化时建了一个包级日志(拿着原来的 os.Stderr),先把它换掉,别让它替我们引用着
			sblog.SetStdLogger(sblog.NewNOPFactory().Logger())
			kept := keepStderrForCrashes(p)
			fakeEmitTargets()
			for i := 0; i < 3; i++ {
				runtime.GC()
				time.Sleep(30 * time.Millisecond)
			}
			crash = make(chan os.Signal, 1)
			signal.Notify(crash, syscall.SIGUSR1)
			_ = os.WriteFile(filepath.Join(dir, "stderr."+pid), []byte(strconv.FormatBool(kept)), 0o644)
		}
		_ = os.WriteFile(filepath.Join(dir, "run."+pid), nil, 0o644)
		if crash != nil {
			select {
			case <-crash:
				var mu sync.Mutex
				mu.Unlock() // fatal error:运行时直接往 fd 2 写原因和栈,recover 不了
			case <-time.After(10 * time.Minute):
			}
			return 0
		}
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
	_ = os.WriteFile(filepath.Join(dir, callerOutName()), []byte(fmt.Sprintf("%d\n%s", code, out)), 0o644)
	return 0
}

// fakeEmitTargets 像守护进程正常跑着时那样往标准错误写连接目标:真的 sing-box 日志(内核在 info 级给每条连接记一行,
// 写到建日志那一刻的 os.Stderr)、标准库 log、直接写 os.Stderr 各一行,都带 secret-target。
func fakeEmitTargets() {
	if f, err := sblog.New(sblog.Options{Context: context.Background(), Options: option.LogOptions{Level: "info", Timestamp: true}}); err == nil {
		_ = f.Start()
		f.NewLogger("inbound/tun[tun-in]").Info("inbound connection to secret-target-singbox.example:443")
		_ = f.Close()
	}
	log.Printf("dial secret-target-stdlog.example:443")
	fmt.Fprintln(os.Stderr, "connect secret-target-stderr.example:443")
}

// callerOutName 调用者把结果写进哪个文件:默认 caller.out,同时跑几个调用者时各用各的(FAKE_OUT)。
func callerOutName() string {
	if n := os.Getenv("FAKE_OUT"); n != "" {
		return n
	}
	return "caller.out"
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
var busyboxApplets = []string{"sh", "pidof", "tr", "sed", "kill", "sleep", "mkdir", "touch", "rm", "cat", "wc", "mv", "chmod", "date"}

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

// localize 脚本里的程序名、rc.func、标记、拉起用的锁、标准错误接的日志、PATH 换到替身环境。
func (r *entwareRig) localize(s string) string {
	r.t.Helper()
	orig := s
	s = strings.Replace(s, "PROCS=godusevpn", "PROCS="+r.name, 1)
	s = strings.Replace(s, "/opt/etc/init.d/rc.func", filepath.Join(r.dir, "rc.func"), 1)
	s = strings.Replace(s, "/opt/var/lib/godusevpn/stopped", r.stopped, 1)
	s = strings.Replace(s, "/opt/var/run/godusevpn.lock", r.lockDir(), 1)
	s = strings.Replace(s, paths.EntwareStderrLog, r.errLog(), 1)
	s = strings.Replace(s, "PATH=/opt/bin:", "PATH="+r.bin+":/opt/bin:", 1)
	if s == orig || strings.Count(s, r.dir) != 4+strings.Count(orig, paths.EntwareStderrLog) || !strings.Contains(s, "PROCS="+r.name+"\n") {
		r.t.Fatalf("脚本模板变了,测试里的替换要跟上:\n%s", s)
	}
	return s
}

// errLog 替身环境里守护进程标准错误接的日志(脚本里是 /opt/var/log/godusevpn.stderr.log;上一级目录一开始不在,脚本自己建)。
func (r *entwareRig) errLog() string { return filepath.Join(r.dir, "log", "godusevpn.stderr.log") }

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
	return r.callerRunOut("caller.out", sub, action, env...)
}

// callerRunOut 同 callerRun,结果写进 dir 下的 outName(几个调用者同时跑时各用各的文件)。
func (r *entwareRig) callerRunOut(outName, sub, action string, env ...string) (code int, out string, alive bool, err error) {
	_ = os.Remove(filepath.Join(r.dir, outName))
	c := exec.Command(r.fake, sub)
	c.Env = r.envWith(append([]string{"FAKE_ACTION=" + action, "FAKE_OUT=" + outName}, env...)...)
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
	b, err := os.ReadFile(filepath.Join(r.dir, outName))
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

// 梅林(Entware):守护进程崩着时用户 godusevpn stop 恰好撞上看门狗整分钟的那一下 —— 看门狗先看了一眼,没有"用户停了"的
// 标记、也没在跑,就去拉起;stop 随后写下标记去停,可刚拉起的守护进程还没出现,stop 一个都没停到就报 done,守护进程随后
// 起来、一直跑着(标记在,看门狗以后也不管它)。现在 stop / kill 和拉起那一段拿同一把锁:拿到锁再写标记、再停;看门狗拿到锁
// 之后再看一次标记。真并发两路(一路像 cru 那样跑 watchdog,一路是同名的调用者跑 godusevpn stop),照常、慢起(拉起之后
// 0.3 秒守护进程才出现,路由器从 U 盘加载程序就是这样)各重复多轮,单数轮让看门狗晚出发几毫秒、双数轮让 stop 晚出发:
// 每轮 stop 都活着拿到 0;stop 返回时、两边都跑完再等一会儿,守护进程都不在;标记留着,锁放掉了。
func TestEntwareStopRacesWatchdog(t *testing.T) {
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gvsrace", busybox)
		r.write("slowexec", "#!/bin/sh\nsleep 0.3\nexec \"$@\"\n", 0o755)
		slow := strings.Replace(r.localize(entwareScript), `PREARGS=""`, `PREARGS="`+filepath.Join(r.dir, "slowexec")+`"`, 1)
		if !strings.Contains(slow, "slowexec") {
			t.Fatal("脚本模板变了:没有 PREARGS=\"\"")
		}
		var bad []string
		for _, v := range []struct{ name, script string }{{"照常", r.localize(entwareScript)}, {"慢起", slow}} {
			r.write("S99godusevpn", v.script, 0o755)
			for i := 0; i < 8; i++ {
				// 守护进程刚崩:没在跑、没有"用户停了"的标记
				for _, pid := range r.alive() {
					_ = syscall.Kill(pid, syscall.SIGKILL)
				}
				waitFor(t, "守护进程没了", func() bool { return len(r.alive()) == 0 && len(entwareDaemonsByScan(r.name)) == 0 })
				_ = os.Remove(r.stopped)
				r.clearLogs()

				var (
					wg             sync.WaitGroup
					wOut, sOut     string
					wErr, sErr     error
					code           int
					stopAlive      bool
					atStop         []int
					wDelay, sDelay time.Duration
					jitter         = time.Duration(i%4) * 3 * time.Millisecond
				)
				if i%2 == 1 {
					wDelay = jitter
				} else {
					sDelay = jitter
				}
				gate := make(chan struct{})
				wg.Add(2)
				go func() { // cru 的看门狗
					defer wg.Done()
					<-gate
					time.Sleep(wDelay)
					wOut, wErr = r.run("watchdog")
				}()
				go func() { // 用户的 godusevpn stop
					defer wg.Done()
					<-gate
					time.Sleep(sDelay)
					code, sOut, stopAlive, sErr = r.callerRun("stop", "stop")
					atStop = entwareDaemonsByScan(r.name) // stop 一返回就数
				}()
				close(gate)
				wg.Wait()
				if wErr != nil || sErr != nil || !stopAlive || code != 0 {
					t.Fatalf("%s 第 %d 轮:看门狗 %v / stop alive=%v code=%d %v\n看门狗: %s\nstop: %s", v.name, i, wErr, stopAlive, code, sErr, wOut, sOut)
				}
				later := r.settled()
				if len(atStop) != 0 || len(later) != 0 || len(entwareDaemonsByScan(r.name)) != 0 {
					bad = append(bad, fmt.Sprintf("%s 第 %d 轮:stop 返回时守护进程 %v,两边跑完过一会儿 %v\n看门狗: %s\nstop: %s",
						v.name, i, atStop, later, strings.TrimSpace(wOut), strings.TrimSpace(sOut)))
					for _, pid := range later {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
				if !r.marked() {
					t.Fatalf("%s 第 %d 轮:stop 之后标记要留着", v.name, i)
				}
				if r.locked() {
					t.Fatalf("%s 第 %d 轮:两边都跑完了,锁应已放掉", v.name, i)
				}
			}
		}
		if len(bad) > 0 {
			t.Fatalf("stop 报 done 了,守护进程却还在跑(%d 轮):\n%s", len(bad), strings.Join(bad, "\n"))
		}
	})
}

// 同时跑的 godusevpn start 和 godusevpn stop(服务在跑):结果要前后一致 —— 守护进程在跑就没有"用户停了"的标记(看门狗照管),
// 停着就留着标记(不然一分钟后看门狗又把它拉起来,stop 白停)。以前 start 在拉起之后、锁外才去掉标记:stop 在中间写下标记、
// 停掉守护进程,start 随后把标记去掉,服务停着、标记没了。现在标记在锁里写、在锁里去,谁后拿到锁就照谁的。
// 真并发两路同名的调用者,单双数轮轮流让一边晚出发几毫秒,重复多轮。
func TestEntwareStartStopConsistent(t *testing.T) {
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gvstst", busybox)
		var bad []string
		for i := 0; i < 12; i++ {
			if len(r.alive()) == 0 {
				if out, err := r.run(startAction(initEntware)); err != nil {
					t.Fatalf("第 %d 轮起服务: %v\n%s", i, err, out)
				}
				waitFor(t, "守护进程起来", func() bool { return len(r.alive()) == 1 })
			}
			_ = os.Remove(r.stopped)

			var (
				wg             sync.WaitGroup
				aOut, bOut     string
				aErr, bErr     error
				aCode, bCode   int
				aAlive, bAlive bool
				aDelay, bDelay time.Duration
				jitter         = time.Duration(i%4) * 3 * time.Millisecond
			)
			if i%2 == 1 {
				aDelay = jitter
			} else {
				bDelay = jitter
			}
			gate := make(chan struct{})
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-gate
				time.Sleep(aDelay)
				aCode, aOut, aAlive, aErr = r.callerRunOut("start.out", "start", startAction(initEntware))
			}()
			go func() {
				defer wg.Done()
				<-gate
				time.Sleep(bDelay)
				bCode, bOut, bAlive, bErr = r.callerRunOut("stop.out", "stop", "stop")
			}()
			close(gate)
			wg.Wait()
			if aErr != nil || bErr != nil || !aAlive || !bAlive || aCode != 0 || bCode != 0 {
				t.Fatalf("第 %d 轮:start alive=%v code=%d %v / stop alive=%v code=%d %v\nstart: %s\nstop: %s", i, aAlive, aCode, aErr, bAlive, bCode, bErr, aOut, bOut)
			}
			n, m := len(r.settled()), r.marked()
			if !(n == 1 && !m) && !(n == 0 && m) {
				bad = append(bad, fmt.Sprintf("第 %d 轮:守护进程 %d 个,标记=%v\nstart: %s\nstop: %s", i, n, m, strings.TrimSpace(aOut), strings.TrimSpace(bOut)))
			}
			if r.locked() {
				t.Fatalf("第 %d 轮:两边都跑完了,锁应已放掉", i)
			}
		}
		if len(bad) > 0 {
			t.Fatalf("start 和 stop 同时跑,结果前后对不上(停着却没标记 = 一分钟后看门狗又拉起来)%d 轮:\n%s", len(bad), strings.Join(bad, "\n"))
		}
	})
}

// openPTY 开一对伪终端(主端、从端):从端给前台跑的守护进程当终端(标准输入、输出、错误都接它),像用户 SSH 上去在终端里跑。
// 主端的输出一直读掉,免得写满了把守护进程卡住。
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("开不了伪终端: %v", err)
	}
	fd := int(m.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		m.Close()
		t.Skipf("解锁伪终端: %v", err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		m.Close()
		t.Skipf("取伪终端号: %v", err)
	}
	s, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		t.Skipf("开伪终端从端: %v", err)
	}
	go func() { _, _ = io.Copy(io.Discard, m) }()
	t.Cleanup(func() { _ = s.Close(); _ = m.Close() })
	return m, s
}

// runScript 测试进程自己跑一遍 script(和 run 一样,调用者不叫守护进程的名字),env 追加的环境变量(后写的优先)。
func (r *entwareRig) runScript(env []string, script string, args ...string) (string, error) {
	sh := "sh"
	if r.busybox {
		sh = filepath.Join(r.bin, "sh")
	}
	c := exec.Command(sh, append([]string{script}, args...)...)
	c.Env = r.envWith(env...)
	b, err := c.CombinedOutput()
	return string(b), err
}

// armedByDaemon 守护进程(Refresh 那一段)补挂的看门狗定时:cru.log 里编号是真名字 godusevpn 的那几行
// (脚本自己挂的编号是替身环境里的名字,分得开)。
func (r *entwareRig) armedByDaemon() []string {
	var out []string
	for _, l := range strings.Split(r.log("cru.log"), "\n") {
		if strings.HasPrefix(l, "a "+name+" ") {
			out = append(out, l)
		}
	}
	return out
}

// 梅林(Entware):开着开机自启时,守护进程启动时(Refresh)给看门狗补挂定时,以前不管是谁起的都挂 —— 用户在终端里前台跑
// godusevpn run 调试,Ctrl-C 停掉之后一分钟内就被看门狗拉到后台去(没有"用户停了"的标记,看门狗当它崩了)。现在只有
// 启动脚本拉起来的才补挂:新旧脚本(连同 Entware 的 rc.func)都在后台拉起、标准输出接 /dev/null,后台命令的标准输入
// 也是 /dev/null;前台跑的连着终端。补挂本来是给旧版本的脚本擦屁股的(更早的脚本起服务时不挂定时),旧脚本拉起来的照样补。
// 真跑:Refresh 那一段用测试程序冒充的守护进程启动时跑(FAKE_REFRESH 指着一份开着自启的已装脚本),cru 是只记账的假货。
//  1. 在伪终端里前台跑(像 SSH 上去跑):不补挂;Ctrl-C 之后要是挂了,像 cron 那样跑一次看门狗,看它会不会被拉到后台;
//  2. 前台跑、输出接管道(godusevpn run | tee …):不补挂;
//  3. 启动脚本拉起(godusevpn start 跑 manual-start):补挂;
//  4. 旧脚本拉起(交给 Entware 的 rc.func 起,它不挂定时):补挂;
//  5. 开机自启关着:脚本拉起的也不补(脚本自己已经挂了)。
func TestEntwareRefreshArmsWatchdogOnlyForScriptLaunch(t *testing.T) {
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gvfgnd", busybox)
		installed := filepath.Join(r.dir, "installed", "S99godusevpn") // 一份"已装的"脚本,Refresh 只读它的开关、换模板
		if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
			t.Fatal(err)
		}
		setInstalled := func(enabled bool) {
			t.Helper()
			s, _ := entwareScriptLike(map[bool]string{true: "ENABLED=yes", false: "ENABLED=no"}[enabled])
			if err := os.WriteFile(installed, []byte(s), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		refresh := "FAKE_REFRESH=" + installed
		path := "PATH=" + r.bin + ":" + os.Getenv("PATH") // 前台跑的从用户的 shell 起,cru 在 PATH 里
		// refreshed 等守护进程 pid 跑完 Refresh 那一段,返回它的结果
		refreshed := func(pid int) string {
			t.Helper()
			f := filepath.Join(r.dir, "refreshed."+strconv.Itoa(pid))
			waitFor(t, "守护进程跑完 Refresh", func() bool { _, err := os.Stat(f); return err == nil })
			b, _ := os.ReadFile(f)
			return string(b)
		}
		// foreground 测试进程直接起一个前台的守护进程(godusevpn run),标准输入输出照 io 接
		foreground := func(tty *os.File, stdout io.Writer) (*exec.Cmd, chan error) {
			t.Helper()
			c := exec.Command(r.fake, "run")
			c.Env = r.envWith(refresh, path)
			if tty != nil {
				c.Stdin, c.Stdout, c.Stderr = tty, tty, tty
			} else {
				c.Stdout, c.Stderr = stdout, stdout
			}
			if err := c.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- c.Wait() }()
			return c, done
		}
		stopAll := func() {
			t.Helper()
			for _, pid := range r.alive() {
				_ = syscall.Kill(pid, syscall.SIGTERM)
			}
			waitFor(t, "守护进程都停掉", func() bool { return len(r.alive()) == 0 })
		}
		setInstalled(true)

		// 1. 伪终端里前台跑:不补挂。Ctrl-C 停掉;挂了的话一分钟内 cron 会跑看门狗(没有标记)——它会被拉到后台
		_, tty := openPTY(t)
		c, done := foreground(tty, nil)
		if res := refreshed(c.Process.Pid); res != "<nil>" {
			t.Fatalf("前台跑的 Refresh 出错: %s", res)
		}
		armed := r.armedByDaemon()
		_ = c.Process.Signal(syscall.SIGINT) // Ctrl-C
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatal("前台跑的守护进程 Ctrl-C 之后没退出")
		}
		if len(armed) > 0 {
			if out, err := r.run("watchdog"); err != nil {
				t.Fatalf("watchdog: %v\n%s", err, out)
			}
			t.Fatalf("前台跑的 godusevpn run 不该补挂看门狗定时(挂了 %q):Ctrl-C 停掉之后,一分钟内看门狗把它拉到了后台 %v", armed, r.settled())
		}
		if got := r.settled(); len(got) != 0 || r.marked() {
			t.Fatalf("前台跑的停掉之后不该有守护进程、也不该动标记: %v 标记=%v", got, r.marked())
		}

		// 2. 前台跑、输出接管道:同样不补挂
		r.clearLogs()
		var buf strings.Builder
		c, done = foreground(nil, &buf)
		if res := refreshed(c.Process.Pid); res != "<nil>" {
			t.Fatalf("Refresh 出错: %s", res)
		}
		if armed := r.armedByDaemon(); len(armed) > 0 {
			t.Fatalf("输出接管道的前台 godusevpn run 也不该补挂看门狗定时: %q", armed)
		}
		_ = c.Process.Signal(syscall.SIGINT)
		<-done

		// 3. 启动脚本拉起(godusevpn start):补挂(脚本自己也挂了一次)
		r.clearLogs()
		if code, out, alive := r.caller("start", startAction(initEntware), refresh); !alive || code != 0 {
			t.Fatalf("start: alive=%v code=%d\n%s", alive, code, out)
		}
		waitFor(t, "守护进程起来", func() bool { return len(r.alive()) == 1 })
		if res := refreshed(r.alive()[0]); res != "<nil>" {
			t.Fatalf("Refresh 出错: %s", res)
		}
		if armed := r.armedByDaemon(); len(armed) != 1 || armed[0] != strings.Join(entwareWatchdog, " ") {
			t.Fatalf("启动脚本拉起的守护进程要补挂看门狗定时: %q(cru.log: %q)", armed, r.log("cru.log"))
		}
		stopAll()

		// 4. 旧脚本拉起(交给 Entware 的 rc.func,它不挂定时):补挂
		r.clearLogs()
		if out, err := r.runScript([]string{refresh, path}, r.legacy(), "start"); err != nil {
			t.Fatalf("旧脚本 start: %v\n%s", err, out)
		}
		waitFor(t, "守护进程起来", func() bool { return len(r.alive()) == 1 })
		if res := refreshed(r.alive()[0]); res != "<nil>" {
			t.Fatalf("Refresh 出错: %s", res)
		}
		if armed := r.armedByDaemon(); len(armed) != 1 {
			t.Fatalf("旧脚本拉起的守护进程要补挂看门狗定时(旧脚本自己不挂): %q", armed)
		}
		stopAll()

		// 5. 开机自启关着:脚本拉起的也不补(脚本起它的时候已经挂上了)
		setInstalled(false)
		r.clearLogs()
		_ = os.Remove(r.stopped)
		if code, out, alive := r.caller("start", startAction(initEntware), refresh); !alive || code != 0 {
			t.Fatalf("start: alive=%v code=%d\n%s", alive, code, out)
		}
		waitFor(t, "守护进程起来", func() bool { return len(r.alive()) == 1 })
		if res := refreshed(r.alive()[0]); res != "<nil>" {
			t.Fatalf("Refresh 出错: %s", res)
		}
		if armed := r.armedByDaemon(); len(armed) != 0 {
			t.Fatalf("开机自启关着时守护进程不补挂: %q", armed)
		}
		stopAll()
	})
}

// 梅林(Entware):守护进程的标准错误以前丢进 /dev/null,崩了看不到原因 —— 运行时崩溃时把原因和全部 goroutine 的栈直接写到
// 标准错误,守护进程自己的 crash.log 只抄得到栈,fatal error 那一行原因不在里面。现在启动脚本把标准错误接到
// /opt/var/log/godusevpn.stderr.log(0600,每次拉起先记一行"== 时间 由 谁 拉起";超过 256KB 下次拉起时整份挪成 .1,
// 只留一份旧的;建不成就照旧接 /dev/null,照样起)。而守护进程平时也往标准错误写:内核(sing-box)在 info 级给每条连接
// 记一行目标,写到建日志那一刻的 os.Stderr —— 连接目标默认不落盘,所以守护进程一启动(KeepStderrForCrashes)看到标准错误
// 接着这个文件,就把 Go 这一层写标准错误的(os.Stderr、标准库 log)改去 /dev/null,文件里只剩崩溃信息。
// 测试程序冒充的守护进程一启动就做这一步,再用真的 sing-box 日志、标准库 log、os.Stderr 各写一行连接目标,收到 USR1 就
// 真的崩一次(fatal error,崩之前先跑几遍垃圾回收)。前台跑的(标准错误接着管道)什么都不改。
func TestEntwareStderrKeepsCrashesOnly(t *testing.T) {
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gverrl", busybox)
		errLog := r.errLog()
		env := []string{"FAKE_STDERR_LOG=" + errLog}
		content := func() string { b, _ := os.ReadFile(errLog); return string(b) }
		headers := func(s string) int { return strings.Count("\n"+s, "\n== ") }
		// kept 等守护进程 pid 处理完标准错误,返回它是不是改了(true / false)
		kept := func(pid int) string {
			t.Helper()
			f := filepath.Join(r.dir, "stderr."+strconv.Itoa(pid))
			waitFor(t, "守护进程处理完标准错误", func() bool { _, err := os.Stat(f); return err == nil })
			b, _ := os.ReadFile(f)
			return string(b)
		}
		start := func(what string) int {
			t.Helper()
			if code, out, alive := r.caller("start", startAction(initEntware), env...); !alive || code != 0 {
				t.Fatalf("%s:start alive=%v code=%d\n%s", what, alive, code, out)
			}
			waitFor(t, what+":守护进程起来", func() bool { return len(r.alive()) == 1 })
			return r.alive()[0]
		}
		stopAll := func() {
			t.Helper()
			if out, err := r.run("stop"); err != nil {
				t.Fatalf("stop: %v\n%s", err, out)
			}
			waitFor(t, "守护进程停掉", func() bool { return len(r.alive()) == 0 })
		}
		fd := func(pid, n int) string {
			s, _ := os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", pid, n))
			return s
		}
		noTargets := func(what, s string) {
			t.Helper()
			if strings.Contains(s, "secret-target") {
				t.Fatalf("%s:连接目标写进了 %s(默认不落盘):\n%s", what, errLog, s)
			}
		}

		// 1. 起服务:标准错误接着日志文件(0600),标准输出是 /dev/null;先记了一行抬头;守护进程写的连接目标没进文件
		//    (它把 Go 这一层的标准错误改走了)
		pid := start("起服务")
		if got := fd(pid, 2); got != errLog {
			t.Fatalf("守护进程的标准错误要接到 %s,得 %q", errLog, got)
		}
		if got := fd(pid, 1); got != os.DevNull {
			t.Fatalf("守护进程的标准输出照旧是 /dev/null,得 %q", got)
		}
		if m := perm(t, errLog); m != 0o600 {
			t.Fatalf("%s 要是 0600,得 %o", errLog, m)
		}
		k := kept(pid)
		s := content()
		if headers(s) != 1 || !strings.Contains(s, " 由 "+startAction(initEntware)+" 拉起") {
			t.Fatalf("拉起之前要记一行抬头:\n%s", s)
		}
		noTargets("正常跑着", s)
		if k != "true" {
			t.Fatalf("标准错误接着崩溃记录文件时,守护进程要把 Go 这一层的标准错误改去 /dev/null(得 %s)", k)
		}

		// 2. 崩了(fatal error):原因那一行和栈都进了文件,连接目标没有
		_ = syscall.Kill(pid, syscall.SIGUSR1)
		waitFor(t, "守护进程崩掉", func() bool { return !fakeAlive(pid, r.name) })
		s = content()
		if !strings.Contains(s, "fatal error: sync: unlock of unlocked mutex") || !strings.Contains(s, "goroutine ") {
			t.Fatalf("崩溃的原因和栈要留在 %s:\n%s", errLog, s)
		}
		noTargets("崩了之后", s)

		// 3. 看门狗拉起:没超过 256KB,接着往同一个文件写,多一行抬头,上次崩溃的记录还在
		if out, err := r.runScript(env, r.script, "watchdog"); err != nil {
			t.Fatalf("watchdog: %v\n%s", err, out)
		}
		waitFor(t, "看门狗拉起", func() bool { return len(r.alive()) == 1 })
		kept(r.alive()[0])
		s = content()
		if headers(s) != 2 || !strings.Contains(s, " 由 watchdog 拉起") || !strings.Contains(s, "fatal error:") {
			t.Fatalf("看门狗拉起要接着写,上次崩溃的记录留着:\n%s", s)
		}
		noTargets("看门狗拉起之后", s)
		stopAll()

		// 4. 超过 256KB:下次拉起时整份挪成 .1(崩溃那段的原因在最前面,不能只留尾巴),新文件只有这次的抬头;
		//    权限被改过的改回 0600
		f, err := os.OpenFile(errLog, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString(strings.Repeat("goroutine 1 [running]: 填充\n", 12000))
		_ = f.Close()
		_ = os.Chmod(errLog, 0o644)
		old := content()
		kept(start("超过 256KB"))
		if b, _ := os.ReadFile(errLog + ".1"); string(b) != old {
			t.Fatalf("超过 256KB 要整份挪成 .1(%d 字节),.1 有 %d 字节", len(old), len(b))
		}
		if s := content(); headers(s) != 1 || strings.Contains(s, "fatal error") {
			t.Fatalf("挪走之后新文件只有这次的抬头:\n%s", s)
		}
		for _, p := range []string{errLog, errLog + ".1"} {
			if m := perm(t, p); m != 0o600 {
				t.Fatalf("%s 要是 0600,得 %o", p, m)
			}
		}
		stopAll()

		// 5. 前台跑的(标准错误接着管道,像终端那样):什么都不改,内核的输出照旧出在标准错误上
		var buf strings.Builder
		c := exec.Command(r.fake, "run")
		c.Env = r.envWith(env...)
		c.Stdout, c.Stderr = &buf, &buf
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		if got := kept(c.Process.Pid); got != "false" {
			t.Fatalf("前台跑的标准错误不是那个文件,不该改(得 %s)", got)
		}
		_ = c.Process.Kill()
		_ = c.Wait()
		if out := buf.String(); !strings.Contains(out, "secret-target-singbox.example") || !strings.Contains(out, "secret-target-stdlog.example") {
			t.Fatalf("前台跑的内核日志照旧出在标准错误上:\n%s", out)
		}

		// 6. 日志目录建不成(那里是个文件之类):照样起,标准错误照旧接 /dev/null
		if err := os.RemoveAll(filepath.Dir(errLog)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Dir(errLog), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		pid = start("日志目录建不成")
		if got := fd(pid, 2); got != os.DevNull {
			t.Fatalf("日志建不成时标准错误照旧接 /dev/null,得 %q", got)
		}
		if got := kept(pid); got != "false" {
			t.Fatalf("标准错误没接那个文件,不该改(得 %s)", got)
		}
		stopAll()

		// 脚本里接的文件和守护进程认的(paths.EntwareStderrLog)是同一个
		if !strings.Contains(entwareScript, "\nERRLOG="+paths.EntwareStderrLog+"\n") {
			t.Fatalf("启动脚本里接标准错误的文件要和 paths.EntwareStderrLog 一致")
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
