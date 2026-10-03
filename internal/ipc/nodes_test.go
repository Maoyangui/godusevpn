package ipc

import (
	"strings"
	"testing"
)

// 按订阅缓存的顺序列,内核里的只是"有没有";订阅删掉、内核里还留着的不列;缓存是空的才按内核的列。
func TestNodeList(t *testing.T) {
	names, in := NodeList([]string{"auto", "香港1", "英国warp"}, []string{"auto", "香港1", "已删节点"})
	if strings.Join(names, ",") != "auto,香港1,英国warp" {
		t.Fatalf("要按订阅缓存列(含刷新新加的、不含订阅已删的): %v", names)
	}
	if !in["香港1"] || in["英国warp"] {
		t.Fatalf("内核里有没有: %v", in)
	}
	if names, _ := NodeList(nil, []string{"auto", "香港1"}); strings.Join(names, ",") != "auto,香港1" {
		t.Fatalf("缓存是空的按内核的列: %v", names)
	}
}
