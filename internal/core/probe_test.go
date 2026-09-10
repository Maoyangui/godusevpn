package core

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// 内核没跑时的测速:只用出站起临时实例;连不上的节点要在父上下文超时内给出 -1,不能挂死也不能 panic。
func TestProbeOfflineUnreachable(t *testing.T) {
	outbounds := []json.RawMessage{
		json.RawMessage(`{"type":"hysteria2","tag":"hk","server":"192.0.2.1","server_port":443,"password":"p","tls":{"enabled":true,"server_name":"a.example"}}`),
		json.RawMessage(`{"type":"anytls","tag":"tw","server":"192.0.2.2","server_port":8443,"password":"p","tls":{"enabled":true,"server_name":"b.example"}}`),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	res := Probe(ctx, outbounds, []string{"hk", "tw", "不存在"}, "", nil)
	if time.Since(start) > 6*time.Second {
		t.Fatal("超时后应尽快返回")
	}
	if len(res) != 2 || res["hk"] != -1 || res["tw"] != -1 {
		t.Fatalf("连不上的节点应为 -1,不存在的节点不出现: %v", res)
	}
}
