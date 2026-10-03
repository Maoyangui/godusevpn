package daemon

import (
	"testing"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 模式切换就地切(只经 Clash API 改 default_mode)的前提是新旧配置只差 default_mode。规则 ↔ 普通全局不满足:
// 全局模式下节点直连规则只放内核自己的进程、默认规则「私网」只在规则模式生效 —— 就地切会留着另一种模式的路由规则,
// 普通全局下别的程序照样能直连节点地址。这种切换必须重建;再点一次当前模式就不必。
func TestModeSwitchLiveOnlyWhenConfigOtherwiseIdentical(t *testing.T) {
	d := newPolicyTestDaemon(t)
	d.settings.Profiles = []settings.Profile{{ID: "p1", Name: "测试", URL: "https://example.invalid/sub"}}
	d.settings.ActiveProfile = "p1"
	d.profiles["p1"] = prof(t, `{"type":"trojan","tag":"a","server":"node.example","server_port":443,"password":"p"}`)

	rule := d.getSettings()
	rule.Mode, rule.NoDirect = settings.ModeRule, false
	global := rule
	global.Mode = settings.ModeGlobal
	if d.modeSwitchIsLive(rule, global) {
		t.Fatal("规则 → 普通全局的配置不只差 default_mode,不能就地切")
	}
	if d.modeSwitchIsLive(global, rule) {
		t.Fatal("普通全局 → 规则同样不能就地切")
	}
	if !d.modeSwitchIsLive(rule, rule) {
		t.Fatal("设置没变时配置应一字不差")
	}
}
