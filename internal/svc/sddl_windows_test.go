package svc

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// 服务权限:已认证用户能查询、能启动,不能停;停止只给控制用户(与 SYSTEM、管理员)。以前所有已认证用户都能停:
// 同机另一个标准账户随时能断主用户的网。坏 SID 跳过,整份描述符仍能解析 —— 0.7.5 那份审计项少一段,
// 整串非法,sc sdset 一直失败(错误被吞),线上服务一直是默认权限:非管理员的托盘既拉不起服务也停不了。
func TestServiceSDDL(t *testing.T) {
	ctl := "S-1-5-21-111-222-333-1001"
	s := serviceSDDL([]string{ctl, "S-1-5-x", ")(A;;GA;;;WD"})
	if _, err := windows.SecurityDescriptorFromString(s); err != nil {
		t.Fatalf("描述符解析失败: %v\n%s", err, s)
	}
	if !strings.Contains(s, "(A;;CCLCSWRPLOCRRC;;;AU)") || strings.Contains(s, "WP;;;AU)") || strings.Contains(s, "RPWPLOCRRC;;;AU)") {
		t.Fatalf("已认证用户应能启动、不能停止: %s", s)
	}
	if !strings.Contains(s, "(A;;CCLCSWRPWPLOCRRC;;;"+ctl+")") {
		t.Fatalf("控制用户应能停止: %s", s)
	}
	if dacl, _, _ := strings.Cut(s, "S:"); strings.Contains(dacl, ";;;WD)") {
		t.Fatalf("坏 SID 混进了权限表: %s", s)
	}
	if strings.Count(s, "(A;") != 6 {
		t.Fatalf("应是 SY、BA、AU、IU、SU 加一个控制用户共 6 条: %s", s)
	}
}
