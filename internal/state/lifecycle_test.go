package state

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Restart 先花时间备新配置(拉订阅最长三分钟)再停旧内核。备的过程中用户点了断开的话,
// 这次重建必须就此打住 —— 不然它会把 wanted 改回 true、隧道自己回来,而磁盘上记的是"不想连",
// 断开时撤掉的闸也没人再装回去,成了"连着但没闸"。
func TestRestartAbortsWhenUserDisconnectsWhilePreparing(t *testing.T) {
	preparing := make(chan struct{})
	release := make(chan struct{})
	var starts, stops int
	var mu sync.Mutex
	first := true

	d := Deps{
		Prepare: func(ctx context.Context) ([]byte, error) {
			mu.Lock()
			isFirst := first
			first = false
			mu.Unlock()
			if isFirst {
				return []byte("cfg"), nil // 第一次连接:立刻返回
			}
			close(preparing) // 告诉测试:重建已经进到备配置这一步了
			<-release        // 卡在这里,让测试有机会去点断开
			return []byte("cfg2"), nil
		},
		Start: func([]byte) error { mu.Lock(); starts++; mu.Unlock(); return nil },
		Stop:  func() error { mu.Lock(); stops++; mu.Unlock(); return nil },
		Alive: func() bool { return true },

		AliveEvery:  20 * time.Millisecond,
		HealthEvery: time.Hour,
		Backoff:     []time.Duration{10 * time.Millisecond},
	}
	m := New(d)
	m.Connect()
	waitUntil(t, func() bool { mu.Lock(); defer mu.Unlock(); return starts == 1 }, "第一次连接没起来")

	done := make(chan error, 1)
	go func() { done <- m.Restart() }()
	<-preparing

	m.Disconnect() // 用户在"正在备配置"的时候点了断开
	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Restart 不该报错: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Restart 没返回,多半是死锁了")
	}

	if m.Wanted() {
		t.Fatal("用户已经点了断开,重建却把 wanted 改回了 true —— 断开被撤销")
	}
	if got := m.Snapshot().Status; got != Disconnected {
		t.Fatalf("状态应停在 disconnected,实际 %s", got)
	}
	mu.Lock()
	gotStarts := starts
	mu.Unlock()
	if gotStarts != 1 {
		t.Fatalf("断开之后不该再起内核,实际起了 %d 次", gotStarts)
	}
}

// Connect / Disconnect / Restart 在真实运行里是并发的:ipc 每条连接一个 goroutine,
// 订阅自动刷新和局域网设备重扫又在守护进程自己的循环里调 Restart。它们不串行的话,
// 旧的 stop() 会和新的 start() 交错,最坏情况是 WaitGroup 误用 panic,
// 而 Connect 从加锁到解锁之间没有 defer —— panic 一抛锁就永远不放,整个守护进程卡死。
func TestLifecycleOpsAreSerialized(t *testing.T) {
	var mu sync.Mutex
	var running bool
	var overlap int

	d := Deps{
		Prepare: func(context.Context) ([]byte, error) { return []byte("cfg"), nil },
		Start: func([]byte) error {
			mu.Lock()
			if running {
				overlap++ // 上一轮还没停就起了新的
			}
			running = true
			mu.Unlock()
			time.Sleep(time.Millisecond)
			return nil
		},
		Stop: func() error {
			mu.Lock()
			running = false
			mu.Unlock()
			time.Sleep(time.Millisecond)
			return nil
		},
		Alive: func() bool { return true },

		AliveEvery:  5 * time.Millisecond,
		HealthEvery: time.Hour,
		Backoff:     []time.Duration{time.Millisecond},
	}
	m := New(d)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			for j := 0; j < 12; j++ {
				switch (k + j) % 3 {
				case 0:
					m.Connect()
				case 1:
					_ = m.Restart()
				default:
					m.Disconnect()
				}
			}
		}(i)
	}
	waited := make(chan struct{})
	go func() { wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(20 * time.Second):
		t.Fatal("并发跑不完,多半死锁了")
	}
	m.Disconnect()

	mu.Lock()
	got := overlap
	mu.Unlock()
	if got != 0 {
		t.Fatalf("旧内核还没停就起了新的,交错了 %d 次", got)
	}
}

func waitUntil(t *testing.T, ok func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}
