package svc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// 应用内升级换完文件要重启服务,而发起的正是服务自己:重启必须整个交给初始化系统,
// 不能是本进程里的 stop 再 start(stop 先把自己杀了,start 永远轮不到)。
func TestRestartHandsOffToInitSystem(t *testing.T) {
	argv, detach := restartCmd(initSystemd, "")
	if strings.Join(argv, " ") != "systemctl --no-block restart godusevpn" || detach {
		t.Fatalf("systemd 要把重启任务交给 systemd 自己(--no-block restart),得到 %v detach=%v", argv, detach)
	}
	for _, k := range []initKind{initProcd, initEntware} {
		argv, detach := restartCmd(k, "/etc/init.d/godusevpn")
		if strings.Join(argv, " ") != "/etc/init.d/godusevpn restart" || !detach {
			t.Fatalf("procd / Entware 要脱离本进程跑脚本的 restart,得到 %v detach=%v", argv, detach)
		}
	}
	if argv, _ := restartCmd(initNone, ""); argv != nil {
		t.Fatal("认不出初始化系统时不该给命令")
	}
}

// 程序起服务交给初始化系统的动作:Entware 用脚本自己的 manual-start(脚本的 start 留给开机时的 rc.unslung,它看开机自启
// 的开关,关着就不起),别的照旧是 start(systemctl start / procd 的 start 本来就不看自启)。
func TestStartAction(t *testing.T) {
	if got := startAction(initEntware); got != "manual-start" || !strings.Contains(entwareScript, "start | manual-start | restart") {
		t.Fatalf("Entware 起服务要用脚本里认的 manual-start,得 %q", got)
	}
	for _, k := range []initKind{initSystemd, initProcd} {
		if got := startAction(k); got != "start" {
			t.Fatalf("systemd / procd 起服务照旧是 start,得 %q", got)
		}
	}
}

// 面板自更新升级上来的旧脚本:按当前模板重写,开机自启的开关照抄。
func TestEntwareScriptRefreshKeepsEnabled(t *testing.T) {
	old := "#!/bin/sh\nENABLED=no\nPROCS=godusevpn\n. /opt/etc/init.d/rc.func\n"
	s, on := entwareScriptLike(old)
	if on || !strings.Contains(s, "ENABLED=no") || strings.Contains(s, "ENABLED=yes") || !strings.Contains(s, "watchdog)") {
		t.Fatalf("自启关着的要保持关着,并换成带看门狗的脚本:\n%s", s)
	}
	if s, on := entwareScriptLike(strings.Replace(old, "ENABLED=no", "ENABLED=yes", 1)); !on || s != entwareScript {
		t.Fatal("自启开着的直接换成当前模板")
	}
}

// 起停服务前先把旧版本写的脚本换成当前模板(旧脚本的起停交给 rc.func 按名字 pidof / killall:start 把调它的本进程
// 当成"已经在跑"而不拉,stop 把本进程一起杀),开机自启的开关照抄;已经是当前模板的不动。先写临时文件再改名,
// 权限照旧(以前临时文件按 0755 写,用户去掉执行位、不让开机自动跑的,一换模板又变回可执行),临时文件不留下。
// 旧版本的临时文件名是 S99godusevpn.tmp:改写被打断时留下来,名字以 S 开头、可执行,开机时 rc.unslung 会把它当成
// 另一份启动脚本跑,脚本已经是当前模板时也要清掉。
func TestRefreshEntwareScriptBeforeStop(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "S99godusevpn")
	old := "#!/bin/sh\nENABLED=no\nPROCS=godusevpn\nARGS=\"run\"\ncase \"$1\" in\nstop | kill)\n\ttouch /opt/var/lib/godusevpn/stopped\n\t;;\nesac\n. /opt/etc/init.d/rc.func\n"
	if err := os.WriteFile(p, []byte(old), 0o700); err != nil {
		t.Fatal(err)
	}
	on, err := refreshEntwareScript(p)
	b, _ := os.ReadFile(p)
	want, _ := entwareScriptLike(old)
	if err != nil || on || string(b) != want || !strings.Contains(string(b), "halt TERM") || !strings.Contains(string(b), "launch \"$1\"") {
		t.Fatalf("旧脚本应换成当前模板、自启保持关着: on=%v err=%v\n%s", on, err, b)
	}
	if m := perm(t, p); m != 0o700 {
		t.Fatalf("换模板时权限要照旧(0700),得 %o", m)
	}
	onlyFile(t, dir, "S99godusevpn", "换模板")
	// 已经是当前模板:不动(同一个文件),旧版本留下的 S99godusevpn.tmp 清掉
	if err := os.WriteFile(p+".tmp", []byte(old[:40]), 0o755); err != nil {
		t.Fatal(err)
	}
	before := inode(t, p)
	if on, err := refreshEntwareScript(p); err != nil || on {
		t.Fatalf("再来一次不该出错: %v %v", on, err)
	}
	if inode(t, p) != before {
		t.Fatal("已经是当前模板的不该再写")
	}
	onlyFile(t, dir, "S99godusevpn", "旧版本留下的临时文件")
	if _, err := refreshEntwareScript(filepath.Join(t.TempDir(), "没有")); err == nil {
		t.Fatal("脚本不在时要报出来")
	}
}

// onlyFile dir 里只有 name 这一个文件(改写脚本没留下临时文件)。
func onlyFile(t *testing.T, dir, name, what string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != name {
		t.Fatalf("%s:目录里只该有 %s,得 %q", what, name, names)
	}
}

// inode p 的 inode 号(看改写是原地改的还是改名盖过去的)。
func inode(t *testing.T, p string) uint64 {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Sys().(*syscall.Stat_t).Ino
}

// perm p 的权限位。
func perm(t *testing.T, p string) os.FileMode {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

// 面板里开关开机自启(SetEnabled)改的是已装的 Entware 启动脚本,而看门狗每分钟、起停时都有 sh 可能正读着它(BusyBox 的 sh
// 边读边执行,一次只读一小段)。以前原地截断重写(os.WriteFile),读的那边会读到空的、半截的脚本;现在和 Refresh 一样先写
// 同目录的临时文件再改名盖过去:正在读的接着读旧的那份,新打开的读新的,任何时候读到的都是完整的一版。权限照旧(用户去掉
// 执行位、不让开机自动跑的,改完还是那样);临时文件不留下。一边反复开关、一边不停地读,读到的每一份都要完整。
func TestSetEntwareEnabledAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "S99godusevpn")
	on, _ := entwareScriptLike("ENABLED=yes")
	off, _ := entwareScriptLike("ENABLED=no")
	if err := os.WriteFile(p, []byte(on), 0o755); err != nil {
		t.Fatal(err)
	}

	// 1. 关掉:内容对、换成了另一个文件(改名盖过去,不是原地改写)、权限照旧、没留下临时文件
	before := inode(t, p)
	if err := setEntwareEnabled(p, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != off {
		t.Fatalf("关掉开机自启后脚本不对:\n%s", b)
	}
	if inode(t, p) == before {
		t.Fatal("要先写临时文件再改名盖过去,不能原地改写(看门狗、起停的 sh 可能正读着)")
	}
	if m := perm(t, p); m != 0o755 {
		t.Fatalf("权限要照旧(0755),得 %o", m)
	}
	onlyFile(t, dir, "S99godusevpn", "关掉开机自启")

	// 2. 用户改过的权限照旧:0700 打开后还是 0700;去掉执行位的(0644)关掉后还是 0644
	for _, c := range []struct {
		mode os.FileMode
		on   bool
		want string
	}{{0o700, true, on}, {0o644, false, off}} {
		if err := os.Chmod(p, c.mode); err != nil {
			t.Fatal(err)
		}
		if err := setEntwareEnabled(p, c.on); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(p); string(b) != c.want {
			t.Fatalf("开关之后脚本不对:\n%s", b)
		}
		if m := perm(t, p); m != c.mode {
			t.Fatalf("权限要照旧(%o),得 %o", c.mode, m)
		}
		onlyFile(t, dir, "S99godusevpn", "开关开机自启")
	}

	// 3. 一边反复开关,一边不停地读(像看门狗、起停时的 sh):读到的每一份都得是完整的开或关那一版
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	var (
		reads, bad int
		samples    []string
		stop       = make(chan struct{})
		done       = make(chan struct{})
	)
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			b, err := os.ReadFile(p)
			reads++
			if err != nil || (string(b) != on && string(b) != off) {
				bad++
				if len(samples) < 5 {
					samples = append(samples, fmt.Sprintf("%d 字节 %v", len(b), err))
				}
			}
		}
	}()
	for i := 0; i < 3000; i++ {
		if err := setEntwareEnabled(p, i%2 == 0); err != nil {
			close(stop)
			<-done
			t.Fatal(err)
		}
	}
	close(stop)
	<-done
	if bad > 0 {
		t.Fatalf("反复开关时读到不完整的脚本 %d 次(共读了 %d 次),例如 %v", bad, reads, samples)
	}
	onlyFile(t, dir, "S99godusevpn", "反复开关之后")

	// 4. 脚本不在:报出来
	if err := setEntwareEnabled(filepath.Join(dir, "没有"), true); err == nil {
		t.Fatal("脚本不在时要报出来")
	}
}

// 开机闸单元:早于联网、不带默认依赖(否则会被排到联网之后),两个落盘文件任一在才跑,跑本程序的 boot-guard;
// OpenWrt 的脚本排在 network(20)之前、只在开机时做。Entware 做不到,不给。
func TestGuardUnitText(t *testing.T) {
	u := guardUnitText(initSystemd, "/usr/local/bin/godusevpn")
	for _, want := range []string{
		"DefaultDependencies=no", "Before=network-pre.target", "Wants=network-pre.target",
		"RequiresMountsFor=/var/lib/godusevpn /usr/local/bin",
		"ConditionPathExists=|/var/lib/godusevpn/guard-boot.nft", "ConditionPathExists=|/var/lib/godusevpn/nic-ipv6-backup.json",
		"Type=oneshot", "ExecStart=/usr/local/bin/godusevpn boot-guard", "WantedBy=sysinit.target",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("systemd 开机闸单元少了 %q:\n%s", want, u)
		}
	}
	p := guardUnitText(initProcd, "/usr/bin/godusevpn")
	if !strings.Contains(p, "START=19") || !strings.Contains(p, "boot() {\n\t/usr/bin/godusevpn boot-guard\n}") || !strings.Contains(p, "start() {\n\treturn 0\n}") {
		t.Fatalf("OpenWrt 开机闸脚本:要排在 network 之前、只在开机时做:\n%s", p)
	}
	if guardUnitText(initEntware, "/opt/bin/godusevpn") != "" {
		t.Fatal("Entware 做不到开机闸")
	}
}

// 服务单元:进程不管怎么没的,都要把内核留下的转发重定向清掉,不然局域网设备连直连都走不了。
func TestSystemdCleansStaleRedirect(t *testing.T) {
	text := mainUnitText(initSystemd, "/usr/local/bin/godusevpn")
	for _, want := range []string{"ExecStart=/usr/local/bin/godusevpn run\n", "ExecStopPost=-/usr/local/bin/godusevpn post-stop\n"} {
		if !strings.Contains(text, want) {
			t.Fatalf("单元里要有 %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "%!") {
		t.Fatalf("模板格式不对:\n%s", text)
	}
}

// procd 的重试次数写 0 才是无限重拉:写 5 的话一小时内崩 5 次就永久停着,严格全局下闸还在、整个局域网断网。
func TestProcdRespawnForever(t *testing.T) {
	if !strings.Contains(procdScript, "procd_set_param respawn 3600 1 0") {
		t.Fatalf("procd 脚本没设无限重拉:\n%s", procdScript)
	}
}
