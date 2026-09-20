package daemon

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Maoyangui/godusevpn/internal/core"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/logx"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/state"
)

// 会话看护(session_policy.go)的记账,和它在状态视图里的露出;外加 MProbeNodes 那道"闸开着就不做直连测速"的门。
//
// 这些测试全部在进程内跑:不监听控制口、不起内核、不开闸、不碰网卡、不出网。
// 直连测速那一段用的节点故意不带服务器地址 —— probeDirect 对这种节点一个包都不发,直接给 -1;
// 于是就算门控坏了、真走到了 probeDirect,测试机也不会往外发探测包,而"有没有走到"仍看得出来(delays 会被写)。

// newPolicyTestDaemon 手工装配一个够跑记账、状态视图与控制口分发的守护进程。
// 不走 NewWithOptions:那会建数据目录、铺规则集、还原系统网络设置。
// 状态机只 New 不 Connect,它的循环不会跑;OnSick 敲的 CheckNow 只是往容量 1 的通道里放一下,没人收也不阻塞。
func newPolicyTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := &Daemon{
		log:      logx.New(filepath.Join(t.TempDir(), "service.log"), 1<<20, 1),
		core:     core.New(nil),
		settings: settings.Default(),
		profiles: map[string]*profile.Profile{},
		fetchErr: map[string]string{}, fetchLink: map[string]string{},
	}
	// Windows 上开着的文件删不掉:不关的话 t.TempDir 的清理会失败(OnRebuild 会写一行日志)
	t.Cleanup(func() { _ = d.log.Close() })
	d.machine = state.New(state.Deps{Logf: d.logf})
	d.server = ipc.NewServer(d.logf)
	d.registerHandlers()
	if d.core.Running() {
		t.Fatal("测试前提不成立:刚 New 出来的内核不该在跑")
	}
	return d
}

// 记账:重建 / 判废各记各的,最近一次重建的节点与原因要记对;一次都没发生过就不给记录。
func TestSessionPolicyAccounting(t *testing.T) {
	t.Run("从没发生过 → 没有记录", func(t *testing.T) {
		d := newPolicyTestDaemon(t)
		if v := d.tunnelView(); v != nil {
			t.Fatalf("一次都没发生过就不该有记录(界面上不显示这一栏),却给了 %+v", *v)
		}
	})

	t.Run("重建一次", func(t *testing.T) {
		d := newPolicyTestDaemon(t)
		before := time.Now().Unix()
		d.OnRebuild("hk", "网络变化")
		v := d.tunnelView()
		if v == nil {
			t.Fatal("重建过一次之后 tunnelView 还是 nil")
		}
		if v.Rebuilds != 1 || v.Sick != 0 {
			t.Fatalf("网络变化拆掉的重建只该记 Rebuilds,不该记 Sick:得 %+v", *v)
		}
		if v.LastNode != "hk" || v.LastReason != "网络变化" {
			t.Fatalf("最近一次重建的节点 / 原因记错了:得 %+v", *v)
		}
		if v.LastAt < before || v.LastAt > time.Now().Unix() {
			t.Fatalf("最近一次重建的时间不对:得 %d,应在 [%d, %d]", v.LastAt, before, time.Now().Unix())
		}
	})

	t.Run("只判废、还没重建也要显示", func(t *testing.T) {
		// 判废之后内核那边会紧接着拆会话、再叫 OnRebuild;但只要判废发生过,就已经是值得给用户看的痕迹了
		d := newPolicyTestDaemon(t)
		d.OnSick("hk", "连续 5 次失败")
		v := d.tunnelView()
		if v == nil {
			t.Fatal("判废过一次却说什么都没发生")
		}
		if v.Sick != 1 || v.Rebuilds != 0 {
			t.Fatalf("判废只记 Sick,重建那一笔由随后的 OnRebuild 记:得 %+v", *v)
		}
		if v.LastNode != "" || v.LastReason != "" || v.LastAt != 0 {
			t.Fatalf("OnSick 不该动「最近一次重建」的字段:得 %+v", *v)
		}
	})

	t.Run("判废后重建:两笔都记、最近一次是判废", func(t *testing.T) {
		d := newPolicyTestDaemon(t)
		d.OnRebuild("hk", "网络变化")
		d.OnSick("jp", "连续 5 次失败")
		v := d.tunnelView()
		if v.LastNode != "hk" || v.LastReason != "网络变化" {
			t.Fatalf("OnSick 不该改「最近一次重建」的节点 / 原因:得 %+v", *v)
		}
		d.OnRebuild("jp", "判废")
		v = d.tunnelView()
		if v.Rebuilds != 2 || v.Sick != 1 {
			t.Fatalf("应累计 2 次重建、其中 1 次判废:得 %+v", *v)
		}
		if v.LastNode != "jp" || v.LastReason != "判废" {
			t.Fatalf("最近一次重建应是 jp / 判废:得 %+v", *v)
		}
	})

	t.Run("快照是副本,改它不影响内部记录", func(t *testing.T) {
		d := newPolicyTestDaemon(t)
		d.OnRebuild("hk", "网络变化")
		v := d.tunnelView()
		v.Rebuilds, v.LastNode = 99, "改坏了"
		if got := d.tunnelView(); got.Rebuilds != 1 || got.LastNode != "hk" {
			t.Fatalf("外面改了快照,内部记录跟着变了:%+v", *got)
		}
	})

	t.Run("并发记账一笔不丢", func(t *testing.T) {
		// 一次网络变化会把几十条会话一起拆掉,回调是并发进来的
		d := newPolicyTestDaemon(t)
		const n = 64
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				d.OnRebuild("hk", "网络变化")
				d.OnSick("hk", "连续 5 次失败")
			}()
		}
		wg.Wait()
		if v := d.tunnelView(); v.Rebuilds != n || v.Sick != n {
			t.Fatalf("并发 %d 次,记成了 %+v", n, *v)
		}
	})
}

// 会话看护的两个回答:内核没跑时谁都不算"在用",也谈不上"想连着"。
func TestSessionPolicyAnswers(t *testing.T) {
	d := newPolicyTestDaemon(t)
	if d.Wanted() {
		t.Fatal("没点过连接、内核也没跑,却说用户想连着")
	}
	for _, tag := range []string{"", "hk", "proxy"} {
		if d.InUse(tag) {
			t.Fatalf("内核没跑,节点 %q 不可能正在用", tag)
		}
	}
}

// 状态视图里的 Tunnel 字段:没发生过是 nil(JSON 里整栏不出现),发生后有值且和记账一致。
func TestStateViewTunnel(t *testing.T) {
	d := newPolicyTestDaemon(t)

	v := d.stateView()
	if v.Tunnel != nil {
		t.Fatalf("没发生过任何重建,Tunnel 却有值:%+v", *v.Tunnel)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"tunnel"`) {
		t.Fatalf("没发生过时 JSON 里不该出现 tunnel 这一栏:%s", b)
	}

	d.OnRebuild("jp", "网络变化")
	v = d.stateView()
	if v.Tunnel == nil {
		t.Fatal("重建过一次之后,状态视图里的 Tunnel 还是 nil")
	}
	if v.Tunnel.Rebuilds != 1 || v.Tunnel.Sick != 0 || v.Tunnel.LastNode != "jp" || v.Tunnel.LastReason != "网络变化" {
		t.Fatalf("状态视图里的记录和记账对不上:%+v", *v.Tunnel)
	}
	if b, _ = json.Marshal(v); !strings.Contains(string(b), `"tunnel"`) {
		t.Fatalf("发生过之后 JSON 里该有 tunnel 这一栏:%s", b)
	}

	// 界面拿的是 GetState 这条控制口方法,和直接调 stateView 应是同一份
	res, err := d.server.Dispatch(ipc.MGetState, nil)
	if err != nil {
		t.Fatalf("GetState 失败: %v", err)
	}
	sv, ok := res.(ipc.StateView)
	if !ok {
		t.Fatalf("GetState 返回的不是 StateView,而是 %T", res)
	}
	if sv.Tunnel == nil || sv.Tunnel.Rebuilds != 1 || sv.Tunnel.LastNode != "jp" {
		t.Fatalf("经控制口拿到的 Tunnel 不对:%+v", sv.Tunnel)
	}
}

// 未连接时的直连测速会从本机直接发 DNS 查询、ICMP、TCP 握手 —— 正是「全局禁直连」的闸要挡的东西,
// 而闸只按进程放行本服务,拦不住自己。所以闸开着、内核又没跑(重启中 / 退避中)的时候,MProbeNodes 必须拒绝,
// 且根本不能走到 probeDirect。
func TestProbeNodesGate(t *testing.T) {
	// 节点故意不带服务器地址:probeDirect 对它不发任何包、直接记 -1。
	// 这样测试自己不出网;而只要 probeDirect 被走到,d.delays 就会被写成 {noaddr: -1},看得出来。
	const noAddr = `{"type":"vless","tag":"noaddr","uuid":"x"}`
	withProfile := func(t *testing.T, d *Daemon, nodes ...string) {
		t.Helper()
		d.settings.Profiles = []settings.Profile{{ID: "p1", Name: "测试", URL: "https://example.invalid/sub"}}
		d.settings.ActiveProfile = "p1"
		d.profiles["p1"] = prof(t, nodes...)
	}
	delays := func(d *Daemon) map[string]int {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.delays
	}

	t.Run("没有订阅:先报没订阅,闸开没开都一样", func(t *testing.T) {
		d := newPolicyTestDaemon(t)
		d.guardOn = true
		_, err := d.server.Dispatch(ipc.MProbeNodes, nil)
		if err == nil || !strings.Contains(err.Error(), "还没有订阅") {
			t.Fatalf("没有订阅应报「还没有订阅」,得: %v", err)
		}
		// 订阅有了、缓存却是空的(一个节点都没拉到)也算没订阅
		withProfile(t, d)
		if _, err := d.server.Dispatch(ipc.MProbeNodes, nil); err == nil || !strings.Contains(err.Error(), "还没有订阅") {
			t.Fatalf("缓存里没节点应报「还没有订阅」,得: %v", err)
		}
		if got := delays(d); got != nil {
			t.Fatalf("没订阅就不该测,delays 却被写了:%v", got)
		}
	})

	t.Run("闸开着、内核没跑:拒绝直连测速,且不走到 probeDirect", func(t *testing.T) {
		d := newPolicyTestDaemon(t)
		withProfile(t, d, noAddr)
		d.guardOn = true
		if v := d.stateView(); v.Guard != "on" {
			t.Fatalf("测试前提:闸应记为开着,状态视图却给 %q", v.Guard)
		}

		res, err := d.server.Dispatch(ipc.MProbeNodes, nil)
		if err == nil {
			t.Fatalf("闸开着、内核没跑,却做了直连测速:%v", res)
		}
		if !strings.Contains(err.Error(), "禁直连") {
			t.Fatalf("拒绝的理由要说清是禁直连的闸在挡,得: %v", err)
		}
		if err.Error() == "服务内部错误" {
			t.Fatalf("处理器 panic 了,被控制口兜住:%v", err)
		}
		// probeDirect 每测出一个就写一个进 delays,最后还会整个赋一次;被门控的那次两处都不该动
		if got := delays(d); got != nil {
			t.Fatalf("被门控的那次不该动 delays,却成了 %v —— 说明 probeDirect 还是被走到了", got)
		}
		if v := d.stateView(); v.Delays != nil {
			t.Fatalf("状态视图里也不该有测速结果:%v", v.Delays)
		}
	})

	t.Run("闸没开、内核没跑:照常直连测速", func(t *testing.T) {
		d := newPolicyTestDaemon(t)
		withProfile(t, d, noAddr)
		if d.guardOn {
			t.Fatal("测试前提:闸该是关着的")
		}
		res, err := d.server.Dispatch(ipc.MProbeNodes, nil)
		if err != nil {
			t.Fatalf("闸没开不该被门控,却报: %v", err)
		}
		m, ok := res.(map[string]int)
		if !ok {
			t.Fatalf("测速结果不是 map[string]int,而是 %T", res)
		}
		if m["noaddr"] != -1 {
			t.Fatalf("没有服务器地址的节点应记 -1,得 %v", m)
		}
		if got := delays(d); got["noaddr"] != -1 {
			t.Fatalf("测速结果没记进状态(界面靠它显示):%v", got)
		}
	})
}

// 隧道起来之前解析节点域名用哪条路:禁直连 + 全局模式下,只允许按地址连的 DoH,
// 「本地 DNS」是 system 或域名时干脆不解析(明文系统解析一个包都不该发);其它模式才退回系统解析器。
// 只看给不给解析函数,不调用它 —— 调了就真的去联网了。
func TestBootResolver(t *testing.T) {
	sealed := func() settings.Settings {
		s := settings.Default()
		s.NoDirect, s.Mode = true, settings.ModeGlobal
		return s
	}
	cases := []struct {
		name string
		mut  func(*settings.Settings)
		want bool // 要不要给解析函数
		why  string
	}{
		{"禁直连 + 全局 + system → 不解析", func(s *settings.Settings) { s.LocalDNS = "system" }, false, "明文系统解析在禁直连下一个包都不该发"},
		{"禁直连 + 全局 + DoH 地址 → 解析", func(s *settings.Settings) { s.LocalDNS = "223.5.5.5" }, true, "按地址连的 DoH 是唯一允许的那条路"},
		{"禁直连 + 全局 + DoH 是域名 → 不解析", func(s *settings.Settings) { s.LocalDNS = "dns.alidns.com" }, false, "解 DoH 自己的域名又得先走系统解析器"},
		{"规则模式 + system → 解析", func(s *settings.Settings) { s.Mode, s.LocalDNS = settings.ModeRule, "system" }, true, "闸没开的模式退回系统解析器"},
		{"直连模式 + system → 解析", func(s *settings.Settings) { s.Mode, s.LocalDNS = settings.ModeDirect, "system" }, true, "闸没开的模式退回系统解析器"},
		{"禁直连关着 + 全局 + system → 解析", func(s *settings.Settings) { s.NoDirect, s.LocalDNS = false, "system" }, true, "开关关了就没有闸"},
		{"规则模式 + DoH 地址 → 解析", func(s *settings.Settings) { s.Mode, s.LocalDNS = settings.ModeRule, "1.1.1.1" }, true, ""},
		{"规则模式 + DoH 是域名 → 解析", func(s *settings.Settings) { s.Mode, s.LocalDNS = settings.ModeRule, "dns.alidns.com" }, true, "闸没开,退回系统解析器"},
	}
	d := &Daemon{} // bootResolver 只看传进去的设置,不碰守护进程的任何字段
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := sealed()
			c.mut(&s)
			got := d.bootResolver(s)
			if (got != nil) != c.want {
				t.Fatalf("给了解析函数 = %v,应为 %v —— %s(NoDirect=%v Mode=%s LocalDNS=%s)", got != nil, c.want, c.why, s.NoDirect, s.Mode, s.LocalDNS)
			}
		})
	}
}
