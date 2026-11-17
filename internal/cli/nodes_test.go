package cli

import (
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/profile"
)

// 节点列表的一行:当前项带 *;测过的写延迟,测不通写"不通",没测过的空着;提示节点(面板放来显示续费 / 用量信息的,
// 列表测速不测)写明它不能上网,不显示成"不通" —— 哪怕状态里还留着它以前测出的数。
func TestNodeLine(t *testing.T) {
	v := ipc.StateView{
		Node:   "香港1",
		Delays: map[string]int{"香港1": 80, "日本": -1, "续费提示": -1},
		Hints:  []string{"00-勿选-流量", "续费提示"},
	}
	for n, want := range map[string]string{
		"香港1":      "* 香港1  80 ms",
		"日本":       "  日本  不通",
		"美国":       "  美国",
		"续费提示":     "  续费提示  提示(只显示续费 / 用量信息,不能上网)",
		"00-勿选-流量": "  00-勿选-流量  提示(只显示续费 / 用量信息,不能上网)",
	} {
		if got := nodeLine(v, n); got != want {
			t.Fatalf("%s: 得 %q,应 %q", n, got, want)
		}
	}
}

// 选中的节点这一轮连不上时 status / select 说一句为什么:关着 IPv6 时选了 IPv6 节点,或者选了提示节点(面板放来显示续费 /
// 用量信息的,地址指向本机,连不上任何地方)—— 网页界面两种都会说,命令行以前只说 IPv6 那种,选了提示节点状态显示
// 已连接、节点就是它,却上不了网,也没有任何说明。
func TestSelectionNotes(t *testing.T) {
	if got := selectionNotes(ipc.StateView{Node: "香港1"}); len(got) != 0 {
		t.Fatalf("选中的能用时不该有说明: %q", got)
	}
	hint := selectionNotes(ipc.StateView{Node: "00-勿选-流量", SelectedHint: true})
	if len(hint) != 1 || !strings.Contains(hint[0], "提示节点") || !strings.Contains(hint[0], "不能上网") {
		t.Fatalf("选了提示节点要说明它不能上网: %q", hint)
	}
	v6 := selectionNotes(ipc.StateView{Node: "v6", SelectedV6: true})
	if len(v6) != 1 || !strings.Contains(v6[0], "IPv6") {
		t.Fatalf("选了连不上的 IPv6 节点要说明: %q", v6)
	}
	// 「允许 IPv6」开着、可当前网络没有 IPv6:说的是网络,不是设置;点名是节点、线路还是全部节点
	net := ipc.StateView{Node: "v6", Nodes: []string{"auto", "v4", "v6"}, SelectedV6NoNet: true}
	net.Settings.Selected = "v6"
	if got := selectionNotes(net); len(got) != 1 || !strings.Contains(got[0], "「v6」只能走 IPv6") || !strings.Contains(got[0], "当前网络没有 IPv6") || strings.Contains(got[0], "允许 IPv6") {
		t.Fatalf("网络没有 IPv6 时的说明: %q", got)
	}
	net.Settings.Selected = ""
	if got := selectionNotes(net); len(got) != 1 || !strings.Contains(got[0], "订阅里的节点全都只能走 IPv6") {
		t.Fatalf("自动选择而节点全是 IPv6 节点: %q", got)
	}
	net.Groups, net.Settings.Selected = []profile.Group{{Tag: "线路甲", Members: []string{"v6"}}}, "线路甲"
	if got := selectionNotes(net); len(got) != 1 || !strings.Contains(got[0], "线路「线路甲」的服务器全都只能走 IPv6") {
		t.Fatalf("选的是线路: %q", got)
	}
}

// 开着 IPv6、网络没有 IPv6 时:只能走 IPv6 的节点测出来是不通,后面写明原因;测得通的(这个判断是看网卡地址推出来的,会有看走眼的时候)照常显示延迟。
func TestNodeLineV6NoNet(t *testing.T) {
	v := ipc.StateView{Node: "auto", Nodes: []string{"auto", "v4", "v6", "v6b", "v6c"}, V6NoNet: []string{"v6", "v6b", "v6c"}, Delays: map[string]int{"v4": 12, "v6": -1, "v6b": 80}}
	for n, want := range map[string]string{
		"v4":  "  v4  12 ms",
		"v6":  "  v6  不通(只能走 IPv6,当前网络没有 IPv6)",
		"v6b": "  v6b  80 ms",
		"v6c": "  v6c",
	} {
		if got := nodeLine(v, n); got != want {
			t.Fatalf("%s: 得 %q,应 %q", n, got, want)
		}
	}
	if got := nodeCountNote(v); got != "(共 4 个节点,其中 3 个只能走 IPv6、当前网络没有 IPv6,连不上)" {
		t.Fatalf("节点数那一句: %q", got)
	}
}
