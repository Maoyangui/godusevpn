package daemon

import (
	"errors"
	"testing"
)

// 内核停下(断线重连、崩溃后退避、服务停止)时要撤掉转发层按接口号装的「经隧道放行」:接口号会被热点 / USB 网卡复用,
// 留着就放行了别的网卡的转发(审计 MB01)。闸没开时不动;撤不掉不影响停机。
//
// guardTunDown 换成假的,guardHold 置上让 stop() 不去还原系统 DNS / 路由:这条测试不碰本机的 WFP 与网络设置
// (数据目录用 TestMain 给的临时目录:守护进程的日志文件开着,单测自己的 TempDir 清不掉)。
func TestStopDropsTunnelForwardPermit(t *testing.T) {
	calls := 0
	var fail error
	old := guardTunDown
	guardTunDown = func() error { calls++; return fail }
	defer func() { guardTunDown = old }()

	d, err := NewWithOptions(Options{NoListen: true})
	if err != nil {
		t.Fatal(err)
	}
	d.guardHold = true

	d.guardOn = false
	_ = d.stop()
	if calls != 0 {
		t.Fatalf("闸没开时不该动转发层: %d", calls)
	}
	d.guardOn = true
	_ = d.stop()
	if calls != 1 {
		t.Fatalf("闸开着时内核一停就该撤隧道放行: %d", calls)
	}
	fail = errors.New("模拟撤不掉")
	if err := d.stop(); err != nil {
		t.Fatalf("撤不掉只记日志,不该让停机失败: %v", err)
	}
}
