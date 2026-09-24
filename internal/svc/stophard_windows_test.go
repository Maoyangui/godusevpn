package svc

import (
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// fakeSvc 按剧本回答 Query;Control 按上一次 Query 报出的状态像服务管理器那样接受或拒收 Stop。
type fakeSvc struct {
	script   []svc.State // Query 依次返回,用完停在最后一个
	i        int
	last     svc.State
	onStop   func(f *fakeSvc) // Stop 被接受时换剧本
	ctlErr   error            // 运行中时 Control 返回的错误(nil = 接受)
	queryErr error
	stops    int // 被接受的 Stop
	rejected int // 因为"启动中 / 停止中"被拒收的 Stop
}

func (f *fakeSvc) Query() (svc.Status, error) {
	if f.queryErr != nil {
		return svc.Status{}, f.queryErr
	}
	f.last = f.script[f.i]
	if f.i < len(f.script)-1 {
		f.i++
	}
	return svc.Status{State: f.last}, nil
}

func (f *fakeSvc) Control(c svc.Cmd) (svc.Status, error) {
	switch f.last {
	case svc.StartPending, svc.StopPending:
		f.rejected++
		return svc.Status{}, windows.ERROR_SERVICE_CANNOT_ACCEPT_CTRL
	case svc.Stopped:
		return svc.Status{}, windows.ERROR_SERVICE_NOT_ACTIVE
	}
	if f.ctlErr != nil {
		return svc.Status{}, f.ctlErr
	}
	f.stops++
	if f.onStop != nil {
		f.onStop(f)
	}
	return svc.Status{State: svc.StopPending}, nil
}

func (f *fakeSvc) play(states ...svc.State) { f.script, f.i = states, 0 }

func fastStop(t *testing.T, resend time.Duration) {
	t.Helper()
	p, r := stopPoll, stopResend
	stopPoll, stopResend = time.Millisecond, resend
	t.Cleanup(func() { stopPoll, stopResend = p, r })
}

// 启动中拒收 Stop:以前发一次就干等(卸载时接着 Delete 只做标记删除,进程活着);现在跑起来后重发,直到停稳。
func TestStopHardRetriesAfterStartPending(t *testing.T) {
	fastStop(t, time.Second)
	f := &fakeSvc{}
	f.play(svc.StartPending, svc.StartPending, svc.Running)
	f.onStop = func(f *fakeSvc) { f.play(svc.StopPending, svc.StopPending, svc.Stopped) }
	if err := stopHard(f, 5*time.Second); err != nil {
		t.Fatalf("应当停稳,得到 %v", err)
	}
	if f.rejected != 2 || f.stops != 1 {
		t.Fatalf("应当拒收 2 次、接受 1 次,实际拒收 %d、接受 %d", f.rejected, f.stops)
	}
}

// 被接受的 Stop 还没报"停止中"之前,不对同一个实例再发(x/sys 处理 Stop 期间收不了新命令,多发的会卡住)。
func TestStopHardNoDoubleStopSameInstance(t *testing.T) {
	fastStop(t, time.Second)
	f := &fakeSvc{}
	f.play(svc.Running)
	f.onStop = func(f *fakeSvc) { f.play(svc.Running, svc.Running, svc.Running, svc.StopPending, svc.Stopped) }
	if err := stopHard(f, 5*time.Second); err != nil {
		t.Fatalf("应当停稳,得到 %v", err)
	}
	if f.stops != 1 {
		t.Fatalf("同一个实例只该发一次 Stop,实际 %d 次", f.stops)
	}
}

// 停下之后又被人拉起一个新实例(轮询没赶上看到"已停止"):过了重发间隔还在跑,再发一次。
func TestStopHardStopsRestartedInstance(t *testing.T) {
	fastStop(t, 20*time.Millisecond)
	f := &fakeSvc{}
	f.play(svc.Running)
	f.onStop = func(f *fakeSvc) {
		f.play(svc.StopPending, svc.Running) // 新实例一直跑
		f.onStop = func(f *fakeSvc) { f.play(svc.StopPending, svc.Stopped) }
	}
	if err := stopHard(f, 5*time.Second); err != nil {
		t.Fatalf("应当停稳,得到 %v", err)
	}
	if f.stops != 2 {
		t.Fatalf("新实例应当再收到一次 Stop,实际共 %d 次", f.stops)
	}
}

// 停不下来就报错(Uninstall 据此不删服务、卸载中止),并带上最后一个真实错误。
func TestStopHardTimeoutKeepsCause(t *testing.T) {
	fastStop(t, time.Millisecond)
	f := &fakeSvc{ctlErr: windows.ERROR_ACCESS_DENIED}
	f.play(svc.Running)
	err := stopHard(f, 30*time.Millisecond)
	if err == nil || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("应当超时并带上 ACCESS_DENIED,得到 %v", err)
	}
	f = &fakeSvc{queryErr: windows.ERROR_INVALID_HANDLE}
	if err := stopHard(f, 30*time.Millisecond); err == nil || !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		t.Fatalf("查询一直失败应当超时并带上原因,得到 %v", err)
	}
}

// 本来就停着:不发 Stop,直接成功。
func TestStopHardAlreadyStopped(t *testing.T) {
	fastStop(t, time.Second)
	f := &fakeSvc{}
	f.play(svc.Stopped)
	if err := stopHard(f, time.Second); err != nil || f.stops+f.rejected != 0 {
		t.Fatalf("已停止时不该发 Stop:err=%v stops=%d rejected=%d", err, f.stops, f.rejected)
	}
}
