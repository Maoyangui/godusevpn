package state

import "testing"

// 服务停止用 Shutdown:循环和内核停了,但"想连"保持原样 —— 停机期间任何还在跑、或排队等锁的同步
// 读到的都是用户原来的意愿,不会按"用户断开"撤闸、还原网卡 IPv6。
func TestShutdownKeepsWanted(t *testing.T) {
	f := &kickDeps{}
	m := newConnected(t, f)
	m.Shutdown()
	if !m.Wanted() {
		t.Fatal("Shutdown 把「想连」清掉了:停机期间的同步会按用户断开处理,撤闸、还原网卡 IPv6")
	}
	if st := m.Snapshot().Status; st != Disconnected {
		t.Fatalf("Shutdown 之后状态应是 Disconnected,实际 %s", st)
	}
	if _, _, _, stops := f.counts(); stops == 0 {
		t.Fatal("Shutdown 没有停内核")
	}
}
