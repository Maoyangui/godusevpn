package svc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// 梅林(Entware)的看门狗:真跑一遍脚本(rc.func / pidof / cru 换成临时目录里的假货)。
// 起服务时挂上定时、去掉"用户停了"的标记;stop 留标记(停进程由脚本自己按进程号做,不交给 rc.func,
// 见 TestEntwareStopSparesCaller);定时看到标记、或服务在跑、或自启关着就不拉,否则拉起来。
func TestEntwareWatchdog(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("没有 sh")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(bin, "pidof"), "#!/bin/sh\n[ -f "+filepath.Join(dir, "running")+" ]\n")
	write(filepath.Join(bin, "cru"), "#!/bin/sh\necho \"$*\" >> "+filepath.Join(dir, "cru.log")+"\n")
	write(filepath.Join(dir, "rc.func"), "echo \"$1\" >> "+filepath.Join(dir, "rc.log")+"\n")
	stopped := filepath.Join(dir, "state", "stopped")
	render := func(enabled bool) string {
		s, _ := entwareScriptLike(map[bool]string{true: "ENABLED=yes", false: "ENABLED=no"}[enabled])
		s = strings.Replace(s, "/opt/etc/init.d/rc.func", filepath.Join(dir, "rc.func"), 1)
		s = strings.Replace(s, "/opt/var/lib/godusevpn/stopped", stopped, 1)
		s = strings.Replace(s, "PATH=/opt/bin:", "PATH="+bin+":/opt/bin:", 1)
		p := filepath.Join(dir, "S99godusevpn")
		write(p, s)
		return p
	}
	run := func(script, action string) (rc, cru string) {
		t.Helper()
		_ = os.Remove(filepath.Join(dir, "rc.log"))
		_ = os.Remove(filepath.Join(dir, "cru.log"))
		if out, err := exec.Command(sh, script, action).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", action, err, out)
		}
		b1, _ := os.ReadFile(filepath.Join(dir, "rc.log"))
		b2, _ := os.ReadFile(filepath.Join(dir, "cru.log"))
		return strings.TrimSpace(string(b1)), strings.TrimSpace(string(b2))
	}
	marked := func() bool { _, err := os.Stat(stopped); return err == nil }
	on := render(true)

	if rc, _ := run(on, "stop"); rc != "" || !marked() {
		t.Fatalf("stop 留下标记,停进程不交给 rc.func(它按名字 killall,会连调用者一起杀): rc=%q 标记=%v", rc, marked())
	}
	if rc, _ := run(on, "watchdog"); rc != "" {
		t.Fatalf("用户停了的服务定时不该拉起来,rc.func 收到 %q", rc)
	}
	rc, cru := run(on, "start")
	if rc != "start" || marked() || cru != "a godusevpn * * * * * /opt/etc/init.d/S99godusevpn watchdog" {
		t.Fatalf("start 要去掉标记、挂上定时: rc=%q 标记=%v cru=%q", rc, marked(), cru)
	}
	if strings.Join(entwareWatchdog, " ") != cru {
		t.Fatalf("守护进程补挂的定时要和脚本里的一致: %q ≠ %q", strings.Join(entwareWatchdog, " "), cru)
	}
	write(filepath.Join(dir, "running"), "")
	if rc, _ := run(on, "watchdog"); rc != "" {
		t.Fatalf("服务在跑时定时不该再拉,rc.func 收到 %q", rc)
	}
	_ = os.Remove(filepath.Join(dir, "running"))
	if rc, _ := run(on, "watchdog"); rc != "start" {
		t.Fatalf("服务没在跑、也不是用户停的(崩了):定时要拉起来,rc.func 收到 %q", rc)
	}
	run(on, "stop")
	if rc, _ := run(on, "restart"); rc != "start" || marked() {
		t.Fatalf("restart(面板升级走这条)要去掉标记,脚本自己停了守护进程后交给 rc.func 的是 start: rc=%q 标记=%v", rc, marked())
	}
	if rc, _ := run(render(false), "watchdog"); rc != "" {
		t.Fatalf("开机自启关着时定时不该拉,rc.func 收到 %q", rc)
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

// 停服务前先把旧版本写的脚本换成当前模板(旧脚本的 stop 交给 rc.func 按名字 killall,会连调 stop 的本进程一起杀),
// 开机自启的开关照抄;已经是当前模板的不动。
func TestRefreshEntwareScriptBeforeStop(t *testing.T) {
	p := filepath.Join(t.TempDir(), "S99godusevpn")
	old := "#!/bin/sh\nENABLED=no\nPROCS=godusevpn\nARGS=\"run\"\ncase \"$1\" in\nstop | kill)\n\ttouch /opt/var/lib/godusevpn/stopped\n\t;;\nesac\n. /opt/etc/init.d/rc.func\n"
	if err := os.WriteFile(p, []byte(old), 0o755); err != nil {
		t.Fatal(err)
	}
	on, err := refreshEntwareScript(p)
	b, _ := os.ReadFile(p)
	want, _ := entwareScriptLike(old)
	if err != nil || on || string(b) != want || !strings.Contains(string(b), "halt TERM") {
		t.Fatalf("旧脚本应换成当前模板、自启保持关着: on=%v err=%v\n%s", on, err, b)
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("临时文件应已改名掉")
	}
	if on, err := refreshEntwareScript(p); err != nil || on {
		t.Fatalf("再来一次不该出错: %v %v", on, err)
	}
	if _, err := refreshEntwareScript(filepath.Join(t.TempDir(), "没有")); err == nil {
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
