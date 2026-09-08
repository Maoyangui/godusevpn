package daemon

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/Maoyangui/godusevpn/internal/profile"
)

// 未连接时的直连测速:TCP 类节点量握手时间,连不上的给 -1;没有服务器地址的也给 -1。
func TestProbeDirectTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	// 找一个肯定没人听的回环端口(公网假地址在开着 TUN 类软件的机器上会被本地握手"接住",不可靠)
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadPort := dead.Addr().(*net.TCPAddr).Port
	dead.Close()
	p := &profile.Profile{
		Tags: []string{"ok", "dead", "empty"},
		Outbounds: []json.RawMessage{
			json.RawMessage(`{"type":"anytls","tag":"ok","server":"127.0.0.1","server_port":` + itoa(port) + `,"password":"p"}`),
			json.RawMessage(`{"type":"trojan","tag":"dead","server":"127.0.0.1","server_port":` + itoa(deadPort) + `,"password":"p"}`),
			json.RawMessage(`{"type":"vless","tag":"empty","uuid":"x"}`),
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	res := probeDirect(ctx, p)
	if res["ok"] < 0 || res["ok"] > 1000 {
		t.Fatalf("本机监听应很快连上: %v", res)
	}
	if res["dead"] != -1 || res["empty"] != -1 {
		t.Fatalf("连不上 / 没地址的应为 -1: %v", res)
	}
}

// ICMP 需要原始套接字权限,没权限时要报"不可用"而不是"不通"(好让调用方退回临时实例)。
func TestPingLoopbackOrUnavailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d, err := pingHost(ctx, "127.0.0.1")
	if err != nil {
		var u *icmpUnavailable
		if !asUnavailable(err, &u) {
			t.Logf("ping 127.0.0.1 失败但不是权限问题: %v", err)
		}
		t.Skip("本机没有 ICMP 权限:", err)
	}
	if d <= 0 || d > time.Second {
		t.Fatalf("回环 ping 时间不合理: %v", d)
	}
}

func asUnavailable(err error, target **icmpUnavailable) bool {
	for err != nil {
		if u, ok := err.(*icmpUnavailable); ok {
			*target = u
			return true
		}
		un, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = un.Unwrap()
	}
	return false
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
