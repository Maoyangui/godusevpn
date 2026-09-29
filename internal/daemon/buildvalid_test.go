package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/core"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/state"
)

// 订阅里混一个本构建建不起来的节点(这里是 naive),其它节点照样要连得上:坏的剔掉,配置干跑通过。
// 全是坏节点时如实报错。只用干跑,不碰系统网络。
func TestBuildValidSkipsUnbuildableNodes(t *testing.T) {
	p := &profile.Profile{Tags: []string{"hk", "nv"}, Outbounds: []json.RawMessage{
		json.RawMessage(`{"type":"hysteria2","tag":"hk","server":"1.2.3.4","server_port":443,"password":"p","tls":{"enabled":true,"server_name":"a.example"}}`),
		json.RawMessage(`{"type":"naive","tag":"nv","server":"1.2.3.7","server_port":443,"username":"u","password":"p","tls":{"enabled":true,"server_name":"n.example"}}`),
	}}
	s := settings.Default()
	s.Selected = "nv"
	c := core.New(nil)
	in := builder.Input{Profile: p, Settings: s, DataDir: t.TempDir(), ClashSecret: "x"}
	cfg, _, skipped, err := buildValid(c, in)
	if err != nil {
		t.Fatalf("混了一个坏节点,其它节点应照样生成出能用的配置: %v", err)
	}
	if len(skipped) != 1 || skipped["nv"] == "" {
		t.Fatalf("应只剔掉 nv: %v", skipped)
	}
	if strings.Contains(string(cfg), `"nv"`) || !strings.Contains(string(cfg), `"hk"`) {
		t.Fatalf("配置里应只剩 hk: %s", cfg)
	}
	if len(p.Tags) != 2 {
		t.Fatal("订阅缓存本身不该被改")
	}

	in.Profile = p.Subset([]string{"nv"})
	if _, _, _, err := buildValid(c, in); err == nil || state.CodeOf(err) != state.CodeConfig {
		t.Fatalf("全是坏节点时应报配置错误: %v", err)
	}
}
