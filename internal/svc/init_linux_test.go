package svc

import (
	"os"
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
// 当成"已经在跑"而不拉,stop 把本进程一起杀),开机自启的开关照抄;已经是当前模板的不动。
func TestRefreshEntwareScriptBeforeStop(t *testing.T) {
	p := filepath.Join(t.TempDir(), "S99godusevpn")
	old := "#!/bin/sh\nENABLED=no\nPROCS=godusevpn\nARGS=\"run\"\ncase \"$1\" in\nstop | kill)\n\ttouch /opt/var/lib/godusevpn/stopped\n\t;;\nesac\n. /opt/etc/init.d/rc.func\n"
	if err := os.WriteFile(p, []byte(old), 0o755); err != nil {
		t.Fatal(err)
	}
	on, err := refreshEntwareScript(p)
	b, _ := os.ReadFile(p)
	want, _ := entwareScriptLike(old)
	if err != nil || on || string(b) != want || !strings.Contains(string(b), "halt TERM") || !strings.Contains(string(b), "launch \"$1\"") {
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
