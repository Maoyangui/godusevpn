package cli

import (
	"reflect"
	"testing"
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
