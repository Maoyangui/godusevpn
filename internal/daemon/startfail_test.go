package daemon

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/logx"
	"github.com/Maoyangui/godusevpn/internal/state"
)

// 内核起不来时的归类。界面只显示错误码对应的一句人话,归错类就等于把用户引到错误的方向;
// 归进最笼统的那一类(E_CORE_START)则等于什么都没说 —— 2026-09-18 用户的索尼电视上,
// 规则集下不到被归成了"内核启动失败",谁也看不出该做什么。
//
// 下面这些错误文本都是内核和系统真会吐出来的原话,不是编的。
func TestClassifyStart(t *testing.T) {
	d := newTestDaemon(t)

	cases := []struct {
		name string
		msg  string
		want string
	}{
		{
			"规则集下不到(电视上那次)",
			`启动内核: initialize rule-set[0]: initial rule-set: geosite-cn: Get "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-cn.srs": context deadline exceeded`,
			state.CodeRuleSet,
		},
		{
			"本地规则集读不出来",
			`启动内核: initialize rule-set[1]: read rule-set geoip-cn: unexpected EOF`,
			state.CodeRuleSet,
		},
		{
			"混合端口被占(Windows 的原话)",
			`启动内核: initialize inbound/mixed[mixed-in]: listen tcp 127.0.0.1:2080: bind: Only one usage of each socket address (protocol/network address/port) is normally permitted.`,
			state.CodePortBusy,
		},
		{
			"Clash API 端口被占(Linux 的原话)",
			`启动内核: create clash-server: listen tcp 127.0.0.1:9090: bind: address already in use`,
			state.CodePortBusy,
		},
		{
			"Windows 上 TUN 网卡建不出来",
			`启动内核: initialize inbound/tun[tun-in]: create wintun adapter: Access is denied.`,
			state.CodeTunDriver,
		},
		{
			"Android 上宿主没给出 TUN",
			`启动内核: initialize inbound/tun[tun-in]: platform: open tun failed`,
			state.CodeTunDriver,
		},
		{
			"macOS 上 utun 建不出来",
			`启动内核: initialize inbound/tun[tun-in]: create utun4: resource busy`,
			state.CodeTunDriver,
		},
		{
			"路由配不上",
			`启动内核: initialize inbound/tun[tun-in]: configure routes: file exists`,
			state.CodeTunDriver, // 这条既提了 tun 又提了 route,归到 TUN 也说得过去
		},
		{
			"网关模式的重定向失败",
			`启动内核: start auto-redirect: nft: no such file or directory`,
			state.CodeRouteConflict,
		},
		{
			"真的说不清是什么",
			`启动内核: 某个从来没见过的毛病`,
			state.CodeCoreStart,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := d.classifyStart(errors.New(c.msg))
			if code := state.CodeOf(got); code != c.want {
				t.Fatalf("归类成了 %s,应该是 %s\n原文:%s", code, c.want, c.msg)
			}
			// 内核的原话必须留在错误里:界面会接在那句人话后面显示,不然又变成"只有一句没用的提示"
			if !strings.Contains(got.Error(), "启动内核") {
				t.Fatalf("把内核的原话丢了:%s", got.Error())
			}
		})
	}
}

// 端口被占时要把端口号说出来,用户才知道去改哪一个。
func TestClassifyStartNamesThePort(t *testing.T) {
	d := newTestDaemon(t)
	err := d.classifyStart(errors.New(`启动内核: initialize inbound/mixed[mixed-in]: listen tcp 127.0.0.1:2080: bind: address already in use`))
	if !strings.Contains(err.Error(), "2080") {
		t.Fatalf("没说清是哪个端口:%s", err.Error())
	}
}

// newTestDaemon 只够跑归类那一段:归类过程中会往日志里写几行,给它一个临时文件就行。
func newTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	return &Daemon{log: logx.New(filepath.Join(t.TempDir(), "service.log"), 1<<20, 1)}
}
