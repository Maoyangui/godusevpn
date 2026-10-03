package daemon

import (
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/state"
)

// 网卡 IPv6 没能停用:只有严格全局拒绝连接;规则 / 普通全局下拒绝等于连 IPv4 也全部直连,改成照常连、首页标出来。
// (nicOff 为假 = 停用没做成,这里不碰任何网卡)
func TestNICProblemBlocksOnlyStrictGlobal(t *testing.T) {
	d := newPolicyTestDaemon(t)
	d.bootWanted.Store(true) // 想连着
	for _, tc := range []struct {
		name     string
		mode     string
		noDirect bool
		block    bool
	}{
		{"规则模式", settings.ModeRule, true, false},
		{"普通全局", settings.ModeGlobal, false, false},
		{"严格全局", settings.ModeGlobal, true, true},
	} {
		s := settings.Default()
		s.Mode, s.NoDirect = tc.mode, tc.noDirect
		d.settings = s
		err := d.nicReady()
		if tc.block {
			if state.CodeOf(err) != state.CodePrivacyNIC {
				t.Fatalf("%s:网卡 IPv6 没停用应拒绝连接,得到 %v", tc.name, err)
			}
			if v := d.stateView(); v.NICWarn != "" {
				t.Fatalf("%s:拒绝连接时不该再挂警告: %q", tc.name, v.NICWarn)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s:不该拒绝连接,得到 %v", tc.name, err)
		}
		if v := d.stateView(); !strings.Contains(v.NICWarn, "未成功停用") {
			t.Fatalf("%s:照常连时首页要标出来,得到 %q", tc.name, v.NICWarn)
		}
	}
	d.bootWanted.Store(false) // 断开后不再挂着
	s := settings.Default()
	d.settings = s
	if err := d.nicReady(); err != nil || d.stateView().NICWarn != "" {
		t.Fatalf("不想连着时既不拒绝也不挂警告: %v %q", err, d.stateView().NICWarn)
	}
}

// 系统网络保护(macOS 接管 DNS、Linux 回包规则)做不成不再拒绝起数据面:不起隧道时流量全部直连,起着只会更少。
// 原因如实标在首页,巡检每半分钟重做。这一处要真起内核才走得到,钉源码结构。
func TestProtectFailureDoesNotBlockStart(t *testing.T) {
	src := readDaemonSource(t)
	start := funcBody(t, src, "func (d *Daemon) start(cfg []byte) error {")
	i := strings.Index(start, "netmode.Protect(")
	if i < 0 {
		t.Fatal("start 里找不到 netmode.Protect —— 代码改了,这条测试要跟着更新")
	}
	block := start[i:]
	block = block[:strings.Index(block, "\n\t}\n")]
	if strings.Contains(block, "return") {
		t.Fatalf("系统网络保护失败不该拒绝起数据面:\n%s", block)
	}
	if !strings.Contains(block, "d.setProtectWarn(err.Error())") {
		t.Fatal("没做成要记下原因给首页看")
	}
	loop := funcBody(t, src, "func (d *Daemon) nicIPv6Loop(ctx context.Context) {")
	if !strings.Contains(loop, "d.protectWarnText() != \"\"") || !strings.Contains(loop, "netmode.Protect(builder.TunName") {
		t.Fatal("连接时没做完的系统网络保护,巡检要重做")
	}
}
