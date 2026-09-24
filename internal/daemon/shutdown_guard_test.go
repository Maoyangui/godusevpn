package daemon

import (
	"testing"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 服务停止时 Run 先把 shuttingDown 置上,再调 machine.Disconnect()(它会先把"想连"清成假,再等状态机当前这一步
// 跑完)。那一步要是正好在 prepare / health 里同步闸和网卡 IPv6,以前会按"用户断开"撤闸、还原 IPv6。
// 现在停机中两者一律不动。
//
// 这里的 Daemon 故意是空壳(machine 为 nil),设置是严格全局 + 停用网卡 IPv6:没有提前返回的话,
// syncGuard / syncNICIPv6 会去读 machine.Wanted() 而空指针崩掉 —— 所以这条测试能抓住"检查被删掉"的回退,
// 而且崩在任何系统调用之前,不会碰本机的闸和网卡。数据目录指到临时目录,多一层保险。
func TestGuardAndNICUntouchedWhileShuttingDown(t *testing.T) {
	t.Setenv("GODUSEVPN_DATA", t.TempDir())
	t.Setenv("GODUSEVPN_CONF", t.TempDir())
	d := &Daemon{}
	d.settingsOK = true
	d.settings = settings.Settings{Mode: settings.ModeGlobal, NoDirect: true, TUN: true, DisableNICIPv6: true}
	d.shuttingDown.Store(true)
	d.syncGuard()
	if err := d.syncNICIPv6(); err != nil {
		t.Fatal(err)
	}
}
