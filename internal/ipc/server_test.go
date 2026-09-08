package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
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
