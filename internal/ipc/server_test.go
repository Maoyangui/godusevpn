package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Maoyangui/godusevpn/internal/state"
)

func TestPipeRoundTrip(t *testing.T) {
	s := NewServer(nil)
	s.Handle("Echo", func(p json.RawMessage) (any, error) {
		in, err := Decode[map[string]string](p)
		if err != nil {
			return nil, err
		}
		return map[string]string{"got": in["msg"]}, nil
	})
	s.Handle("Fail", func(json.RawMessage) (any, error) { return nil, &CallError{Code: "E_X", Msg: "坏了"} })
	s.Handle("Boom", func(json.RawMessage) (any, error) { panic("x") })
	if err := s.Listen(); err != nil {
		t.Skipf("本机不能建管道: %v", err)
	}
	defer s.Close()
	var out map[string]string
	if err := Call(context.Background(), "Echo", map[string]string{"msg": "你好"}, &out); err != nil {
		t.Fatal(err)
	}
	if out["got"] != "你好" {
		t.Fatalf("回显不对: %v", out)
	}
	err := Call(context.Background(), "Fail", nil, nil)
	var ce *CallError
	if !errors.As(err, &ce) || ce.Code != "E_X" {
		t.Fatalf("错误码应透传: %v", err)
	}
	if err := Call(context.Background(), "Boom", nil, nil); err == nil {
		t.Fatal("处理器 panic 应变成错误而不是断连")
	}
	if err := Call(context.Background(), "Nope", nil, nil); err == nil {
		t.Fatal("未知方法应报错")
	}
	s.Close()
	if err := Call(context.Background(), "Echo", nil, nil); !errors.Is(err, ErrNoService) {
		t.Fatalf("服务关了应报 ErrNoService: %v", err)
	}
}

// 处理器跑得比连接的空闲期限长(换新配置要好几分钟):应答照样写得回去,调用方在自己的期限内收得到。
// 以前整条连接只给 60 秒,处理器还没跑完连接就到期,调用方只能等到自己超时。
func TestSlowHandlerStillAnswers(t *testing.T) {
	s := NewServer(nil)
	s.idle = 200 * time.Millisecond
	s.Handle("Slow", func(json.RawMessage) (any, error) {
		time.Sleep(800 * time.Millisecond)
		return "done", nil
	})
	if err := s.Listen(); err != nil {
		t.Skipf("本机不能建管道: %v", err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out string
	if err := Call(ctx, "Slow", nil, &out); err != nil || out != "done" {
		t.Fatalf("处理器跑得比空闲期限长,应答没回来: %q %v", out, err)
	}
}

// 等多久不短于守护进程换新配置的上限。
func TestSlowCallTimeoutCoversPrepare(t *testing.T) {
	if SlowCallTimeout <= state.PrepareTimeout {
		t.Fatalf("SlowCallTimeout=%v 不比备配置的上限 %v 长", SlowCallTimeout, state.PrepareTimeout)
	}
}
