package daemon

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/core"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// hintProfile 线路「hk」部署在两台服务器上,组里还混着一条指向 127.0.0.1 的「续费提示」;订阅最前面是 m-ui 式的用量提示节点。
func hintProfile(t *testing.T) *profile.Profile {
	t.Helper()
	p := prof(t,
		`{"type":"shadowsocks","tag":"00-勿选-流量:1.0/100.0 重置:3天","server":"127.0.0.1","server_port":1,"method":"aes-128-gcm","password":"dummy"}`,
		`{"type":"socks","tag":"hk-续费提示","server":"127.0.0.1","server_port":88}`,
		`{"type":"socks","tag":"hk-A","server":"192.0.2.1","server_port":1080}`,
		`{"type":"socks","tag":"hk-B","server":"192.0.2.2","server_port":1080}`,
		`{"type":"socks","tag":"v6","server":"2001:db8::1","server_port":1080}`,
	)
	p.Groups = []profile.Group{{Tag: "hk", Members: []string{"hk-续费提示", "hk-A", "hk-B"}}}
	return p
}

// 提示节点(地址只指向本机,见 builder.HintNodes)不进自动选择:生成的配置干跑通过,auto 与线路组里都没有它,
// 测速前 urltest 先用的第一个成员因此不会是它;proxy 里照样列着,手动选得上。
func TestHintNodesConfigValid(t *testing.T) {
	p := hintProfile(t)
	s := settings.Default()
	s.IPv6 = true // 只看提示节点
	s.Selected = "hk"
	cfg, rep, skipped, err := buildValid(core.New(nil), builder.Input{Profile: p, Settings: s, DataDir: t.TempDir(), ClashSecret: "x"})
	if err != nil || len(skipped) != 0 {
		t.Fatalf("带提示节点的订阅应生成出干跑通过的配置: %v %v", err, skipped)
	}
	if want := []string{"00-勿选-流量:1.0/100.0 重置:3天", "hk-续费提示"}; !reflect.DeepEqual(rep.Hints, want) {
		t.Fatalf("Report.Hints: %v", rep.Hints)
	}
	var c struct {
		Outbounds []struct {
			Tag       string   `json:"tag"`
			Outbounds []string `json:"outbounds"`
		} `json:"outbounds"`
	}
	if err := json.Unmarshal(cfg, &c); err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, o := range c.Outbounds {
		got[o.Tag] = o.Outbounds
	}
	if !reflect.DeepEqual(got["auto"], []string{"hk-A", "hk-B", "v6"}) || !reflect.DeepEqual(got["hk"], []string{"hk-A", "hk-B"}) {
		t.Fatalf("自动选择与线路组里不该有提示节点: auto=%v hk=%v", got["auto"], got["hk"])
	}
	if len(got["proxy"]) != 1+1+len(p.Tags) {
		t.Fatalf("proxy 里节点一个不少: %v", got["proxy"])
	}
}

// 关掉「线路自动」(内核没跑)固定到组里第一台:组里排第一的是提示节点时跳过它,和连不上的 IPv6 节点一样。
func TestSetLineAutoSkipsHintNodes(t *testing.T) {
	d := lineDaemon(t)
	d.profiles["p1"] = hintProfile(t)
	d.settings.Selected, d.settings.LineAuto = "hk", true
	want := []string{"00-勿选-流量:1.0/100.0 重置:3天", "hk-续费提示", "v6"} // 提示节点在前;关着 IPv6,写成 IPv6 地址的不用(不分平台)
	if got := d.autoSkippedNow(d.getSettings()); !reflect.DeepEqual(got, want) {
		t.Fatalf("自动选择不用的节点: %v,应 %v", got, want)
	}
	raw, _ := json.Marshal(map[string]bool{"on": false})
	res, err := d.Dispatch(ipc.MSetLineAuto, raw)
	if err != nil {
		t.Fatal(err)
	}
	if v := res.(ipc.StateView); v.Settings.Selected != "hk-A" || v.Settings.LineAuto {
		t.Fatalf("关掉线路自动应固定到 hk-A(跳过排第一的提示节点): %q %v", v.Settings.Selected, v.Settings.LineAuto)
	}
}
