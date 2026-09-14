package profile

import (
	"encoding/json"
	"testing"
)

func mk(t *testing.T, nodes ...string) *Profile {
	t.Helper()
	p := &Profile{}
	for _, n := range nodes {
		var meta struct {
			Tag string `json:"tag"`
		}
		if err := json.Unmarshal([]byte(n), &meta); err != nil {
			t.Fatal(err)
		}
		p.Outbounds = append(p.Outbounds, json.RawMessage(n))
		p.Tags = append(p.Tags, meta.Tag)
	}
	return p
}

// 刷新订阅后要决定动不动隧道,靠的就是这几个比较:字节不同但内容相同不能算"变了",顺序变了也不算。
func TestSameIgnoresFormattingAndOrder(t *testing.T) {
	a := mk(t, `{"type":"hysteria2","tag":"hk","server":"1.2.3.4","server_port":443}`, `{"type":"anytls","tag":"jp","server":"5.6.7.8","server_port":8443}`)
	b := mk(t,
		"{\n  \"server_port\": 8443,\n  \"server\": \"5.6.7.8\",\n  \"tag\": \"jp\",\n  \"type\": \"anytls\"\n}",
		"{ \"server\":\"1.2.3.4\", \"type\":\"hysteria2\", \"tag\":\"hk\", \"server_port\":443 }")
	if !Same(a, b) {
		t.Fatalf("只是格式和顺序不同,应算相同:%+v", Diff(a, b))
	}
	if c := Diff(a, b); !c.Empty() {
		t.Fatalf("应无差别:%+v", c)
	}
}

func TestDiffCounts(t *testing.T) {
	a := mk(t, `{"tag":"hk","server":"1.1.1.1"}`, `{"tag":"jp","server":"2.2.2.2"}`, `{"tag":"us","server":"3.3.3.3"}`)
	b := mk(t, `{"tag":"hk","server":"1.1.1.1"}`, `{"tag":"jp","server":"9.9.9.9"}`, `{"tag":"sg","server":"4.4.4.4"}`)
	c := Diff(a, b)
	if c.Added != 1 || c.Removed != 1 || c.Changed != 1 {
		t.Fatalf("应 +1 −1 改1,得 %+v", c)
	}
	if Same(a, b) {
		t.Fatal("有差别不该算相同")
	}
	// nil 当空订阅
	if c := Diff(nil, b); c.Added != 3 || c.Removed != 0 {
		t.Fatalf("nil → b 应全是新增,得 %+v", c)
	}
	if c := Diff(a, nil); c.Removed != 3 {
		t.Fatalf("a → nil 应全是移除,得 %+v", c)
	}
	if !Same(nil, nil) {
		t.Fatal("两个 nil 应相同")
	}
}

func TestNodeLookup(t *testing.T) {
	p := mk(t, `{"tag":"hk","server":"1.1.1.1","server_port":443}`)
	got, ok := p.Node("hk")
	if !ok || got != `{"server":"1.1.1.1","server_port":443,"tag":"hk"}` {
		t.Fatalf("应返回规整后的 JSON,得 %q %v", got, ok)
	}
	if _, ok := p.Node("nope"); ok {
		t.Fatal("不存在的节点不该找到")
	}
	var nilP *Profile
	if _, ok := nilP.Node("hk"); ok {
		t.Fatal("nil 订阅里什么都没有")
	}
}
