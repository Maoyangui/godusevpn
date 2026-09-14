package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/profile"
)

func prof(t *testing.T, nodes ...string) *profile.Profile {
	t.Helper()
	p := &profile.Profile{}
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

// 刷新订阅不该掐断正在用的连接:只有当前节点被删或改了参数才重连。
func TestDecideRefresh(t *testing.T) {
	hk := `{"tag":"hk","type":"hysteria2","server":"1.1.1.1","server_port":443}`
	jp := `{"tag":"jp","type":"anytls","server":"2.2.2.2","server_port":8443}`
	running := prof(t, hk, jp)
	for _, c := range []struct {
		name    string
		running *profile.Profile
		fresh   *profile.Profile
		current string
		want    refreshOutcome
		why     string
	}{
		{"没在跑", nil, prof(t, hk), "hk", refreshNoop, "没在跑"},
		{"一模一样", running, prof(t, jp, hk), "hk", refreshNoop, "无变化"},
		{"只是格式不同", running, prof(t, "{ \"server_port\":443, \"server\":\"1.1.1.1\", \"type\":\"hysteria2\", \"tag\":\"hk\" }", jp), "hk", refreshNoop, "无变化"},
		{"别的节点变了", running, prof(t, hk, `{"tag":"jp","type":"anytls","server":"9.9.9.9","server_port":8443}`), "hk", refreshDeferred, "未受影响"},
		{"加了节点", running, prof(t, hk, jp, `{"tag":"sg","server":"3.3.3.3"}`), "hk", refreshDeferred, "+1"},
		{"删了别的节点", running, prof(t, hk), "hk", refreshDeferred, "−1"},
		{"当前节点被删", running, prof(t, jp), "hk", refreshReconnect, "已被面板移除"},
		{"当前节点改了参数", running, prof(t, `{"tag":"hk","type":"hysteria2","server":"1.1.1.1","server_port":8443}`, jp), "hk", refreshReconnect, "参数已变"},
		{"查不到当前节点", running, prof(t, hk), "", refreshDeferred, "查不到当前节点"},
		{"自动选择落在的节点没了", running, prof(t, hk), "jp", refreshReconnect, "已被面板移除"},
	} {
		got, why := decideRefresh(c.running, c.fresh, c.current)
		if got != c.want || !strings.Contains(why, c.why) {
			t.Fatalf("%s:应 %d(含 %q),得 %d %q", c.name, c.want, c.why, got, why)
		}
	}
}

func TestChangeText(t *testing.T) {
	if s := changeText(profile.Change{Added: 2, Removed: 1, Changed: 3}); s != "+2 −1 改 3" {
		t.Fatalf("得 %q", s)
	}
	if s := changeText(profile.Change{Removed: 1}); s != "−1" {
		t.Fatalf("得 %q", s)
	}
}
