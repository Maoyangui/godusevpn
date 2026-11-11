package cli

import (
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
