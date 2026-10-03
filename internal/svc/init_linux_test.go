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
// 起服务时挂上定时、去掉"用户停了"的标记;stop 留标记;定时看到标记、或服务在跑、或自启关着就不拉,否则拉起来。
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

	if rc, _ := run(on, "stop"); rc != "stop" || !marked() {
		t.Fatalf("stop 要照常交给 rc.func 并留下标记: rc=%q 标记=%v", rc, marked())
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
	if rc, _ := run(on, "restart"); rc != "restart" || marked() {
		t.Fatalf("restart(面板升级走这条)要去掉标记: rc=%q 标记=%v", rc, marked())
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

// procd 的重试次数写 0 才是无限重拉:写 5 的话一小时内崩 5 次就永久停着,严格全局下闸还在、整个局域网断网。
func TestProcdRespawnForever(t *testing.T) {
	if !strings.Contains(procdScript, "procd_set_param respawn 3600 5 0") {
		t.Fatalf("procd 脚本没设无限重拉:\n%s", procdScript)
	}
}
