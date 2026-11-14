package cli

import (
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/ipc"
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
}
