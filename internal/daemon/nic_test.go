package daemon

import (
	"os"
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/netmode"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 网卡层面的 IPv6 和「全局禁直连」的闸是同一套语义:跟的是"用户想不想连着",不是"内核在不在跑"。
// 这套判断错一格,轻则用户的 IPv6 永久回不来,重则该藏起来的公网地址露在外面,所以穷举钉死。
func TestNICIPv6Action(t *testing.T) {
	cases := []struct {
		name string
		want bool // 此刻该不该关着
		on   bool // 现在是不是我们关着的
		act  nicAction
		why  string
	}{
		{"该关、还没关 → 去关", true, false, nicDisable, "刚点连接、或关机期间冒出新网卡"},
		{"该关、已经关着 → 不动", true, true, nicNoop, "内核崩了在重试、切订阅重连、服务重启,都属于这一格;再跑一遍那段脚本既慢又会抖网卡"},
		{"不该关、还关着 → 还原", false, true, nicRestore, "用户点了断开 / 关掉开关 / 打开 IPv6"},
		{"不该关、也没关 → 不动", false, false, nicNoop, "没连过,或平台动不了物理网卡"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nicIPv6Action(c.want, c.on); got != c.act {
				t.Fatalf("判成 %v,应为 %v —— %s", got, c.act, c.why)
			}
		})
	}
}

// wantNICOff 三个设置条件缺一不可;安卓上还要再叠一条平台能力(那边应用动不了物理网卡)。
func TestWantNICOff(t *testing.T) {
	base := func() settings.Settings {
		s := settings.Default()
		s.TUN, s.IPv6, s.DisableNICIPv6 = true, false, true
		return s
	}
	if got := wantNICOff(base()); got != netmode.NICIPv6Manageable() {
		t.Fatalf("三个条件都满足时应跟着平台能力走:得到 %v,平台能力 %v", got, netmode.NICIPv6Manageable())
	}
	if !netmode.NICIPv6Manageable() {
		t.Skip("这个平台动不了物理网卡,下面几条不适用")
	}
	off := func(mut func(*settings.Settings)) bool {
		s := base()
		mut(&s)
		return wantNICOff(s)
	}
	if off(func(s *settings.Settings) { s.TUN = false }) {
		t.Fatal("不是 TUN 模式就不该动网卡")
	}
	if off(func(s *settings.Settings) { s.IPv6 = true }) {
		t.Fatal("用户明确要 IPv6 时不该动网卡")
	}
	if off(func(s *settings.Settings) { s.DisableNICIPv6 = false }) {
		t.Fatal("开关关着时不该动网卡")
	}
}

// 这一条守着设计本身:网卡 IPv6 的判断里**不许**出现"内核在不在跑"。
// 一旦有人把它改回跟着内核走,内核崩了 / 重连 / 关机重启那几十秒,公网 v6 地址就又露出来了 ——
// 2026-09-17 那次重启的日志里就是这么漏的(18:08:00 停服务还原、18:08:53 才关回去)。
func TestNICIPv6FollowsWantedNotCoreRunning(t *testing.T) {
	src := readDaemonSource(t)
	fn := funcBody(t, src, "func (d *Daemon) nicIPv6Wanted() bool {")
	if contains(fn, "core.Running") {
		t.Fatal("nicIPv6Wanted 里不该看内核在不在跑:它必须和闸一样只看用户的连接意愿")
	}
	if !contains(fn, "machine.Wanted") {
		t.Fatal("nicIPv6Wanted 必须看 machine.Wanted():这才是和闸一致的那个判断")
	}
	stop := funcBody(t, src, "func (d *Daemon) stop() error {")
	if contains(stop, "RestoreNICIPv6") {
		t.Fatal("stop() 里不该还原网卡 IPv6:内核停了不代表用户不想连了")
	}
	start := funcBody(t, src, "func (d *Daemon) start(cfg []byte) error {")
	if contains(start, "DisableNICIPv6") {
		t.Fatal("start() 里不该自己停用网卡 IPv6:那一步归 syncNICIPv6 管,prepare 里已经对齐过")
	}
}

// ---- 上面那条"守着设计"的测试要用的小工具:直接读源码,比起造一个完整的 Daemon 便宜得多 ----

func readDaemonSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("daemon.go")
	if err != nil {
		t.Fatalf("读不到 daemon.go: %v", err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// funcBody 从函数签名那一行取到顶格的 "}" 为止。
func funcBody(t *testing.T, src, sig string) string {
	t.Helper()
	i := strings.Index(src, sig)
	if i < 0 {
		t.Fatalf("daemon.go 里找不到 %q —— 函数被改名或改签名了,这条测试要跟着更新", sig)
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n}\n"); j >= 0 {
		return rest[:j]
	}
	return rest
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
