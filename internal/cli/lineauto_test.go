package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/profile"
)

// settings 里的 lineAuto=on|off 单独拦下来(服务保存整份设置时沿用原值,只认 SetLineAuto),其余照常一起保存。
func TestTakeLineAuto(t *testing.T) {
	rest, on, err := takeLineAuto([]string{"ipv6=on", "lineAuto=on", "logDays=9"})
	if err != nil || on == nil || !*on || !reflect.DeepEqual(rest, []string{"ipv6=on", "logDays=9"}) {
		t.Fatalf("得 %v %v %v", rest, on, err)
	}
	if _, on, err = takeLineAuto([]string{"lineAuto=off"}); err != nil || on == nil || *on {
		t.Fatalf("off: %v %v", on, err)
	}
	if rest, on, _ = takeLineAuto([]string{"mode=global"}); on != nil || len(rest) != 1 {
		t.Fatal("没写 lineAuto 时不该调开关")
	}
	if _, _, err = takeLineAuto([]string{"lineAuto=maybe"}); err == nil {
		t.Fatal("值写错要报出来")
	}
}

// status 的 IPv6 提示点名是哪个节点 / 哪条线路(自动选择时说节点全是这种),并说清楚去哪儿打开。
func TestV6Hint(t *testing.T) {
	v := ipc.StateView{Nodes: []string{"auto", "香港1-香港", "v6节点"}, Groups: []profile.Group{{Tag: "香港1", Members: []string{"香港1-香港", "v6节点"}}}}
	for sel, want := range map[string]string{"v6节点": "「v6节点」只能走 IPv6", "香港1": "线路「香港1」的服务器全都只能走 IPv6", "": "订阅里的节点全都只能走 IPv6", "不在了": "订阅里的节点全都只能走 IPv6"} {
		v.Settings.Selected = sel
		got := v6Hint(v)
		if !strings.Contains(got, want) || !strings.Contains(got, "设置 → 隐私") || !strings.Contains(got, "ipv6=on") {
			t.Fatalf("选 %q:%s", sel, got)
		}
	}
}
