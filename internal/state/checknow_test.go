package state

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// 会话看护判定当前节点的会话已废时会敲一下 CheckNow,让状态机立刻做一次健康检查,
// 不等三分钟一次的定时器;而且第一次不通就把节奏收紧到 DegradedEvery,
// 确认"真的不对劲"只要再等一个短周期。这几条测试钉住这两点。
//
// HealthEvery 设成一小时:定时器那条路在测试里永远不会自己响,
// 于是每一次 Health 调用都只能来自 CheckNow 或收紧后的短周期,数得清。

type kickDeps struct {
	mu               sync.Mutex
	healthErr        error       // Health 返回什么;nil = 通
	failFor          int         // 只让前 failFor 次不通,之后自动转好(0 = 一直按 healthErr 来)
	healths          int         // Health 被叫了几次
	healthAt         []time.Time // 每次 Health 被叫的时刻
	seen             []Status    // 每次 Health 被叫时状态机当时的状态(反映上一次检查处理完的结果)
	recovers         int
	recoverOK        bool
	healAfterRecover bool // 自救"换好了"之后 Health 就转好,模拟换到了一条通的线路
	healthsAtRecover int  // 叫自救那一刻 Health 已经被叫了几次
	starts           int
	stops            int
	m                *Machine // Health 里要看状态机当时的状态
}

func (f *kickDeps) counts() (healths, recovers, starts, stops int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.healths, f.recovers, f.starts, f.stops
}

func (f *kickDeps) deps() Deps {
	return Deps{
		Prepare: func(context.Context) ([]byte, error) { return []byte("{}"), nil },
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
			// 状态机调 Health 时不持 mu,这里看快照是安全的;
			// 此刻的状态就是上一次检查处理完的结果,比在测试里轮询稳得多。
			var cur Status
			if f.m != nil {
				cur = f.m.Snapshot().Status
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			f.healths++
			f.healthAt = append(f.healthAt, time.Now())
			f.seen = append(f.seen, cur)
			if f.failFor > 0 && f.healths > f.failFor {
				return nil
			}
			return f.healthErr
		},
		Recover: func(context.Context) bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.recovers++
			f.healthsAtRecover = f.healths
			if f.recoverOK && f.healAfterRecover {
				f.healthErr = nil
			}
			return f.recoverOK
		},
		AliveEvery:    time.Hour, // 别让"内核死了"那条路插进来
		HealthEvery:   time.Hour, // 常规定时器在测试里永远不响
		DegradedEvery: 20 * time.Millisecond,
		Backoff:       []time.Duration{5 * time.Millisecond},
	}
}

// 起一台已经连上、还没做过任何健康检查的状态机。
func newConnected(t *testing.T, f *kickDeps) *Machine {
	t.Helper()
	m := New(f.deps())
	f.m = m
	m.Connect()
	waitUntil(t, func() bool { return m.Snapshot().Status == Connected }, "没连上")
	if healths, _, _, _ := f.counts(); healths != 0 {
		t.Fatalf("HealthEvery 是一小时,连上之后不该自己做健康检查,实际做了 %d 次", healths)
	}
	return m
}

// 线路好好的:CheckNow 立刻触发一次健康检查,状态仍是已连接,之后也不会因此多查。
func TestCheckNowRunsHealthImmediately(t *testing.T) {
	f := &kickDeps{}
	m := newConnected(t, f)
	defer m.Disconnect()

	m.CheckNow()
	waitUntil(t, func() bool { healths, _, _, _ := f.counts(); return healths == 1 },
		"CheckNow 之后没有立刻做健康检查 —— 看护已经有证据了,还得等三分钟")
	if st := m.Snapshot().Status; st != Connected {
		t.Fatalf("检查通过,状态应该还是已连接,实际是 %s", st)
	}
	// 通了就没有理由收紧节奏:再等几个 DegradedEvery,不该再有新的检查
	time.Sleep(100 * time.Millisecond)
	if healths, _, _, _ := f.counts(); healths != 1 {
		t.Fatalf("线路正常时 CheckNow 只该查一次,实际查了 %d 次", healths)
	}
}

// 线路不通:CheckNow 触发第 1 次失败后节奏立刻收紧 —— 第 2 次检查在 DegradedEvery 内到来,
// 不用等 HealthEvery;第 2 次失败进 Degraded。好了之后回到已连接、回到正常节奏。
func TestCheckNowFirstFailureTightensSchedule(t *testing.T) {
	f := &kickDeps{healthErr: errors.New("测不通"), failFor: 2}
	m := newConnected(t, f)
	defer m.Disconnect()

	m.CheckNow()
	waitUntil(t, func() bool { healths, _, _, _ := f.counts(); return healths >= 1 }, "CheckNow 没有触发健康检查")
	// 第 2 次必须自己来(测试不再敲 CheckNow),而且要在短周期内,不是一小时后
	waitUntil(t, func() bool { healths, _, _, _ := f.counts(); return healths >= 2 },
		"第一次不通后没有收紧节奏 —— 第二次检查要等到一小时后的常规定时器")
	// 第 3 次(转好的那次)也来了
	waitUntil(t, func() bool { healths, _, _, _ := f.counts(); return healths >= 3 }, "进 Degraded 之后没有按短周期复查")

	f.mu.Lock()
	gap := f.healthAt[1].Sub(f.healthAt[0])
	seen := append([]Status(nil), f.seen...)
	f.mu.Unlock()
	if gap >= time.Second {
		t.Fatalf("第二次检查隔了 %s 才来,没有收紧到 DegradedEvery", gap)
	}
	// seen[i] 是第 i+1 次检查开始时的状态,即前一次检查处理完的结果
	if seen[1] != Connected {
		t.Fatalf("只失败一次不该进 Degraded,第二次检查时状态是 %s", seen[1])
	}
	if seen[2] != Degraded {
		t.Fatalf("连续两次失败应该进 Degraded,第三次检查时状态是 %s", seen[2])
	}

	// 第 3 次通了:回到已连接,失败计数清零,健康检查回到一小时的正常节奏
	waitUntil(t, func() bool { return m.Snapshot().Status == Connected }, "好了之后应该回到已连接")
	time.Sleep(100 * time.Millisecond)
	if healths, _, starts, _ := f.counts(); healths != 3 || starts != 1 {
		t.Fatalf("好了就该回到正常节奏、不重建:实际查了 %d 次、起了 %d 次内核", healths, starts)
	}
}

// 一直不通:从 CheckNow 开始连续失败到第 degradedGiveUp 次就叫自救;
// 自救说"换好了"就再给一轮观察期,不重建连接。
func TestCheckNowGivesRecoveryAChance(t *testing.T) {
	f := &kickDeps{healthErr: errors.New("测不通"), recoverOK: true, healAfterRecover: true}
	m := newConnected(t, f)
	defer m.Disconnect()

	m.CheckNow()
	waitUntil(t, func() bool { _, rec, _, _ := f.counts(); return rec >= 1 }, "连续不通却没有叫自救")
	f.mu.Lock()
	at := f.healthsAtRecover
	f.mu.Unlock()
	if at != degradedGiveUp {
		t.Fatalf("应该在第 %d 次失败时叫自救,实际是第 %d 次", degradedGiveUp, at)
	}
	waitUntil(t, func() bool { return m.Snapshot().Status == Connected }, "换线之后应该回到已连接")
	time.Sleep(100 * time.Millisecond)
	if _, rec, starts, stops := f.counts(); starts != 1 || stops != 0 || rec != 1 {
		t.Fatalf("自救成功就不该重建连接:起了 %d 次内核、停了 %d 次、叫了 %d 次自救", starts, stops, rec)
	}
}

// 自救也没用(返回 false):必须重建连接,而不是永远停在 Degraded。
func TestCheckNowRebuildsWhenRecoveryFails(t *testing.T) {
	f := &kickDeps{healthErr: errors.New("测不通"), recoverOK: false}
	m := newConnected(t, f)
	defer m.Disconnect()

	m.CheckNow()
	waitUntil(t, func() bool { _, _, starts, _ := f.counts(); return starts >= 2 },
		"自救没用却没有重建连接 —— 用户会永远卡在「已连接 · 节点不稳」")
	if _, rec, _, stops := f.counts(); rec == 0 || stops == 0 {
		t.Fatalf("重建之前应该先叫自救、再把内核停干净:叫了 %d 次自救、停了 %d 次", rec, stops)
	}
}

// 没在连:CheckNow 不能阻塞、不能 panic,也不会凭空做检查。
// 断开期间敲的 CheckNow 是对上一条连接的判定:下次 Connect 起来不该凭它平白多查一次。
func TestCheckNowStaleKickDroppedOnConnect(t *testing.T) {
	f := &kickDeps{}
	m := New(f.deps())
	f.m = m
	m.CheckNow() // 没在连:token 留在通道里
	m.Connect()
	defer m.Disconnect()
	waitUntil(t, func() bool { return m.Snapshot().Status == Connected }, "没连上")
	time.Sleep(100 * time.Millisecond)
	if healths, _, _, _ := f.counts(); healths != 0 {
		t.Fatalf("断开期间攒下的 CheckNow 不该在新连接上触发检查,实际查了 %d 次", healths)
	}
	m.CheckNow() // 连着的时候敲才算数
	waitUntil(t, func() bool { healths, _, _, _ := f.counts(); return healths == 1 }, "连着时 CheckNow 没触发检查")
}

func TestCheckNowWhenNotConnected(t *testing.T) {
	f := &kickDeps{}
	m := New(f.deps())
	f.m = m

	done := make(chan struct{})
	go func() {
		defer close(done)
		// 敲好几下:kick 容量只有 1,少了 default 分支第二下就会卡死
		for i := 0; i < 5; i++ {
			m.CheckNow()
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("没在连的时候 CheckNow 卡住了")
	}
	time.Sleep(50 * time.Millisecond)
	if st := m.Snapshot().Status; st != Disconnected {
		t.Fatalf("没有 Connect,状态不该变,实际是 %s", st)
	}
	if healths, _, starts, _ := f.counts(); healths != 0 || starts != 0 {
		t.Fatalf("没在连就不该做检查或起内核:查了 %d 次、起了 %d 次", healths, starts)
	}
}
