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

// Shutdown 之后,任何在途的 Restart / 重连(它们的 Prepare 不持 opMu,可能跨过 Shutdown)都不许再把内核拉起来 ——
// 否则服务已经返回、控制口已关,数据面又被起了一遍,而且不会走正常的停止路径。
func TestNoStartAfterShutdown(t *testing.T) {
	f := &kickDeps{}
	m := newConnected(t, f)
	m.Shutdown()
	_, _, startsBefore, _ := f.counts()
	m.connect()
	if err := m.Restart(); err != nil {
		t.Fatal(err)
	}
	if err := m.RestartChecked(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, _, starts, _ := f.counts(); starts != startsBefore {
		t.Fatalf("Shutdown 之后内核又被起了 %d 次", starts-startsBefore)
	}
	if st := m.Snapshot().Status; st != Disconnected {
		t.Fatalf("Shutdown 之后状态应保持 Disconnected,实际 %s", st)
	}
}
