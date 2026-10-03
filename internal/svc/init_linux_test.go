package svc

import (
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

// procd 的重试次数写 0 才是无限重拉:写 5 的话一小时内崩 5 次就永久停着,严格全局下闸还在、整个局域网断网。
func TestProcdRespawnForever(t *testing.T) {
	if !strings.Contains(procdScript, "procd_set_param respawn 3600 5 0") {
		t.Fatalf("procd 脚本没设无限重拉:\n%s", procdScript)
	}
}
