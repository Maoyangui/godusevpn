package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// 托盘的点击回调是 systray 在窗口过程(wndProc)里**同步**调的:回调不返回,托盘的消息循环
// 就不处理任何消息,左右键一起失灵。这条钉住「点击处理必须立刻还给 wndProc」。
//
// 2026-09-22 真机上就栽在这条链上:托盘窗口被 IsHungAppWindow 判为 hung,图标和提示照常更新
// (Shell_NotifyIcon 不经消息循环),但左右键都没反应。
func TestAsyncReturnsBeforeHandlerFinishes(t *testing.T) {
	release := make(chan struct{})
	ran := make(chan struct{})
	handler := async(func() {
		<-release
		close(ran)
	})

	handler() // 这一下就是 wndProc 里的那一调:必须立刻返回,不能等处理跑完
	select {
	case <-ran:
		t.Fatal("点击处理在调用方里同步跑完了:托盘的消息循环会被它卡住")
	default:
	}

	close(release)
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("放行之后点击处理没有真的跑起来")
	}
}

// 挪到 goroutine 之后就没有了 wndProc 天然的串行,所以同一项必须自己去重:
// 否则「恢复网络」连点几下会叠出几个 UAC 提权框,「退出」会并发跑两遍停服务。
func TestAsyncIgnoresRepeatClicksWhileBusy(t *testing.T) {
	release := make(chan struct{})
	var runs atomic.Int32
	handler := async(func() {
		runs.Add(1)
		<-release
	})

	handler()
	waitFor(t, 5*time.Second, func() bool { return runs.Load() == 1 }, "第一次点击没跑起来")
	handler()
	handler()
	if got := runs.Load(); got != 1 {
		t.Fatalf("在途时的重复点击应当被忽略,实际跑了 %d 次", got)
	}

	// 上一次跑完之后,这一项要能再次触发 —— 去重只针对"在途",不是一次性开关
	close(release)
	waitFor(t, 5*time.Second, func() bool {
		handler()
		return runs.Load() >= 2
	}, "在途结束后再点没有触发")
}

// 每次 async 各带一个在途标志:慢的那一项不能把别的项一起挡住。
func TestAsyncHandlersAreIndependent(t *testing.T) {
	stuck := make(chan struct{})
	slow := async(func() { <-stuck })
	var fast atomic.Int32
	quick := async(func() { fast.Add(1) })

	slow() // 这一项卡住不返回
	quick()
	waitFor(t, 5*time.Second, func() bool { return fast.Load() == 1 }, "另一项被卡住的那项挡住了")
	close(stuck)
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}
