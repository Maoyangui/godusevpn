package state

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fake struct {
	mu       sync.Mutex
	prepErr  error
	startErr error
	alive    atomic.Bool
	starts   int32
	stops    int32
	health   error
	changes  []Status
}

func (f *fake) deps() Deps {
	return Deps{
		Prepare: func(ctx context.Context) ([]byte, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return []byte("cfg"), f.prepErr
		},
		Start: func([]byte) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.startErr != nil {
				return f.startErr
			}
			atomic.AddInt32(&f.starts, 1)
			f.alive.Store(true)
			return nil
		},
		Stop:  func() error { atomic.AddInt32(&f.stops, 1); f.alive.Store(false); return nil },
		Alive: func() bool { return f.alive.Load() },
		Health: func(context.Context) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.health
		},
		OnChange:    func(s Snapshot) { f.mu.Lock(); f.changes = append(f.changes, s.Status); f.mu.Unlock() },
		AliveEvery:  20 * time.Millisecond,
		HealthEvery: 30 * time.Millisecond,
		// 不设的话会取默认的 30 秒:降级之后健康检查会放慢,这个用例就等不到恢复了
		DegradedEvery: 30 * time.Millisecond,
		Backoff:       []time.Duration{20 * time.Millisecond, 40 * time.Millisecond},
	}
}

func waitFor(t *testing.T, m *Machine, st Status) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if m.Snapshot().Status == st {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等 %s 超时,当前 %+v", st, m.Snapshot())
}

func TestConnectDisconnect(t *testing.T) {
	f := &fake{}
	m := New(f.deps())
	m.Connect()
	waitFor(t, m, Connected)
	if !m.Wanted() || atomic.LoadInt32(&f.starts) != 1 {
		t.Fatal("应启动一次并记为想连")
	}
	m.Disconnect()
	if s := m.Snapshot(); s.Status != Disconnected || s.Wanted {
		t.Fatalf("断开后: %+v", s)
	}
	if atomic.LoadInt32(&f.stops) < 1 {
		t.Fatal("断开应停内核")
	}
}

func TestRetryWithBackoffThenSucceed(t *testing.T) {
	f := &fake{}
	f.prepErr = Errf(CodeProfileNet, "拉不到订阅")
	m := New(f.deps())
	m.Connect()
	waitFor(t, m, Failed)
	if s := m.Snapshot(); s.Code != CodeProfileNet || s.Retries < 1 {
		t.Fatalf("失败应带错误码与次数: %+v", s)
	}
	f.mu.Lock()
	f.prepErr = nil
	f.mu.Unlock()
	waitFor(t, m, Connected)
	if m.Snapshot().Retries != 0 {
		t.Fatal("成功后重试计数应清零")
	}
	m.Disconnect()
}

func TestCoreDeathReconnects(t *testing.T) {
	f := &fake{}
	m := New(f.deps())
	m.Connect()
	waitFor(t, m, Connected)
	f.alive.Store(false) // 内核死了
	waitFor(t, m, Failed)
	if m.Snapshot().Code != CodeCoreCrash {
		t.Fatalf("应记为内核崩溃: %+v", m.Snapshot())
	}
	waitFor(t, m, Connected)
	if atomic.LoadInt32(&f.starts) < 2 {
		t.Fatal("应自动重启内核")
	}
	m.Disconnect()
}

// 隐私核查失败(闸缺失 / 网卡 v6 冒出来)只降级、**不拆隧道**:拆了没有任何一种情况变好 ——
// 闸还在时只是白断网,网卡上的地址也不会因此消失;闸缺失或规则模式下拆了等于全部直连。
// m29 在这里立刻 Stop 并重连,每次重连 attempt 归零,泄漏治不好、网却一直断。
// 错误码要原样留在状态里,让界面能说清楚是隐私核查没过,而不是"内核崩溃"。
func TestPrivacyFailureDegradesWithoutStopping(t *testing.T) {
	f := &fake{}
	m := New(f.deps())
	m.Connect()
	waitFor(t, m, Connected)
	f.mu.Lock()
	f.health = Errf(CodePrivacyNIC, "网卡 IPv6 保护丢失")
	f.mu.Unlock()
	waitFor(t, m, Degraded)
	if got := m.Snapshot(); got.Code != CodePrivacyNIC {
		t.Fatalf("隐私核查失败的错误码要原样保留: %+v", got)
	}
	if atomic.LoadInt32(&f.stops) != 0 {
		t.Fatal("隐私核查失败不能拆隧道:闸缺失时拆了等于全部直连")
	}
	f.mu.Lock()
	f.health = nil
	f.mu.Unlock()
	waitFor(t, m, Connected)
	if atomic.LoadInt32(&f.stops) != 0 {
		t.Fatal("修好之后也不该有过任何一次停机")
	}
	m.Disconnect()
}

func TestDegradedAndRecover(t *testing.T) {
	f := &fake{}
	m := New(f.deps())
	m.Connect()
	waitFor(t, m, Connected)
	f.mu.Lock()
	f.health = errors.New("节点不通")
	f.mu.Unlock()
	waitFor(t, m, Degraded)
	if m.Snapshot().Status != Degraded || atomic.LoadInt32(&f.stops) != 0 {
		t.Fatal("健康检查失败只降级,不断线")
	}
	f.mu.Lock()
	f.health = nil
	f.mu.Unlock()
	waitFor(t, m, Connected)
	m.Disconnect()
}

func TestRestartOnlyWhenWanted(t *testing.T) {
	f := &fake{}
	m := New(f.deps())
	m.Restart()
	if atomic.LoadInt32(&f.starts) != 0 {
		t.Fatal("不想连时 Restart 不该启动")
	}
	m.Connect()
	waitFor(t, m, Connected)
	m.Restart()
	waitFor(t, m, Connected)
	if atomic.LoadInt32(&f.starts) != 2 || !m.Wanted() {
		t.Fatalf("Restart 应停了再起并保持想连: starts=%d", f.starts)
	}
	m.Disconnect()
}

// Restart 要先备好新配置再停旧的:准备期间旧内核还活着,备不出来就不动它。
func TestRestartPreparesBeforeStopping(t *testing.T) {
	f := &fake{}
	var preparedAlive atomic.Bool
	d := f.deps()
	prep := d.Prepare
	d.Prepare = func(ctx context.Context) ([]byte, error) {
		if f.alive.Load() {
			preparedAlive.Store(true)
		}
		return prep(ctx)
	}
	m := New(d)
	m.Connect()
	waitFor(t, m, Connected)
	if err := m.Restart(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, m, Connected)
	if !preparedAlive.Load() {
		t.Fatal("新配置应在旧内核还活着的时候准备")
	}
	if atomic.LoadInt32(&f.stops) != 1 || atomic.LoadInt32(&f.starts) != 2 {
		t.Fatalf("应停一次、起两次,得 stops=%d starts=%d", f.stops, f.starts)
	}

	// 新配置备不出来:返回错误,旧内核原样连着
	f.mu.Lock()
	f.prepErr = errors.New("订阅坏了")
	f.mu.Unlock()
	if err := m.Restart(); err == nil {
		t.Fatal("准备失败应返回错误")
	}
	if m.Snapshot().Status != Connected || atomic.LoadInt32(&f.stops) != 1 {
		t.Fatalf("准备失败不该动旧内核:%+v stops=%d", m.Snapshot(), f.stops)
	}
	m.Disconnect()
}
