package state

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// 线路连着但一点流量都过不去(手动选定的节点夜里被墙、机房掉线)时,状态机必须有出路。
// 早先没有:连续两次健康检查失败进 Degraded 之后就再也不动了 —— 界面永久停在
// 「已连接 · 节点不稳」,不会换线也不会重连,开着「全局禁直连」时连直连都没有,
// 遥控器用户只能自己去点断开再连。这几条测试钉住那条出路。

type fakeDeps struct {
	mu        sync.Mutex
	prepares  int
	starts    int
	stops     int
	healthOK  bool
	recovers  int
	recoverOK bool
}

func (f *fakeDeps) counts() (prepares, starts, stops, recovers int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prepares, f.starts, f.stops, f.recovers
}

func (f *fakeDeps) deps() Deps {
	return Deps{
		Prepare: func(context.Context) ([]byte, error) {
			f.mu.Lock()
			f.prepares++
			f.mu.Unlock()
			return []byte("{}"), nil
		},
		Start: func([]byte) error {
			f.mu.Lock()
			f.starts++
			f.mu.Unlock()
			return nil
		},
		Stop: func() error {
			f.mu.Lock()
			f.stops++
			f.mu.Unlock()
			return nil
		},
		Alive: func() bool { return true },
		Health: func(context.Context) error {
			f.mu.Lock()
			ok := f.healthOK
			f.mu.Unlock()
			if ok {
				return nil
			}
			return errors.New("测不通")
		},
		Recover: func(context.Context) bool {
			f.mu.Lock()
			f.recovers++
			ok := f.recoverOK
			f.mu.Unlock()
			return ok
		},
		AliveEvery:    time.Hour, // 别让"内核死了"那条路插进来
		HealthEvery:   5 * time.Millisecond,
		DegradedEvery: 5 * time.Millisecond,
		Backoff:       []time.Duration{5 * time.Millisecond},
	}
}

// 一直不通,而且自救也没用:必须重建连接,而不是永远停在 Degraded。
func TestDegradedRebuildsConnection(t *testing.T) {
	f := &fakeDeps{}
	m := New(f.deps())
	m.Connect()
	defer m.Disconnect()

	waitUntil(t, func() bool { _, starts, _, _ := f.counts(); return starts >= 2 },
		"一直不通却没有重建连接 —— 用户会永远卡在「已连接 · 节点不稳」")
	if _, _, stops, _ := f.counts(); stops == 0 {
		t.Fatal("重建之前应该先把内核停干净")
	}
	if _, _, _, rec := f.counts(); rec == 0 {
		t.Fatal("放弃之前应该先给守护进程一次自救的机会")
	}
}

// 自救成功(换到一条通的线路)就再给一轮观察期,别急着断开重来。
func TestDegradedGivesRecoveryAChance(t *testing.T) {
	f := &fakeDeps{recoverOK: true}
	m := New(f.deps())
	m.Connect()
	defer m.Disconnect()

	waitUntil(t, func() bool { _, _, _, rec := f.counts(); return rec >= 1 }, "没有叫自救")
	// 自救说"换好了",于是恢复健康
	f.mu.Lock()
	f.healthOK = true
	f.mu.Unlock()

	waitUntil(t, func() bool { return m.Snapshot().Status == Connected }, "换线之后应该回到已连接")
	if _, starts, _, _ := f.counts(); starts != 1 {
		t.Fatalf("自救成功就不该重建连接,实际起了 %d 次内核", starts)
	}
}

// 中途自己好了:回到已连接,失败计数清零,健康检查回到正常节奏。
func TestDegradedRecoversOnItsOwn(t *testing.T) {
	f := &fakeDeps{}
	m := New(f.deps())
	m.Connect()
	defer m.Disconnect()

	waitUntil(t, func() bool { return m.Snapshot().Status == Degraded }, "连续失败应该进 Degraded")
	f.mu.Lock()
	f.healthOK = true
	f.mu.Unlock()
	waitUntil(t, func() bool { return m.Snapshot().Status == Connected }, "恢复之后应该回到已连接")
}
