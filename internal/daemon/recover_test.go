package daemon

import (
	"context"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Maoyangui/godusevpn/internal/ipc"
)

// fakeNodeServer 本机回环上的假节点服务器(socks 出站连它)。前 hang 条连接收下之后憋 hold 再断 —— 测到这台的那一轮测速
// 就持续这么久(模拟被墙、丢包的节点:要等到超时才失败);之后的连接一收就断(秒失败)。返回端口和一共收下的连接数。
func fakeNodeServer(t *testing.T, hang int, hold time.Duration) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if int(accepted.Add(1)) <= hang {
				go func() {
					time.Sleep(hold)
					_ = c.Close()
				}()
				continue
			}
			_ = c.Close()
		}
	}()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port), &accepted
}

// startRound 在后台对测速组起一轮测速(像定时测速那样),等它真的测到假节点(收到第一条连接)再返回。
// 内核刚起来时它自己的那一轮(PostStart)可能正好占着组:那时叫 GroupTest 会立刻回空结果、什么都不测,得再叫。
func startRound(t *testing.T, d *Daemon, group string, accepted *atomic.Int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	go func() {
		for accepted.Load() == 0 && time.Now().Before(deadline) {
			_, _ = d.core.GroupTest(context.Background(), group)
			time.Sleep(5 * time.Millisecond)
		}
	}()
	waitAccepted(t, accepted, 1)
}

// settle 等内核启动时各测速组自己的那一轮(PostStart,成员都预记了"刚测过",瞬间就完)过去,别和测试要数的那几轮叠在一起。
func settle(t *testing.T, d *Daemon, groups ...string) {
	t.Helper()
	time.Sleep(100 * time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for _, g := range groups {
		for {
			busy, known := d.core.GroupBusy(g)
			if !known || !busy {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("测速组 %s 一直在测", g)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// waitAccepted 等假节点服务器收到第 n 条连接(那一轮测速确实开始测它了)。
func waitAccepted(t *testing.T, accepted *atomic.Int32, n int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for accepted.Load() < n {
		if time.Now().After(deadline) {
			t.Fatalf("测速一直没连到假节点(收到 %d 条)", accepted.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// urltestOut 测速组出站(和 builder 生成的一样关掉 sing-box 自己的定时测速)。
func urltestOut(tag, members, url string) string {
	return `{"type":"urltest","tag":"` + tag + `","outbounds":[` + members + `],"url":"` + url + `","interval":"24h","idle_timeout":"25h"}`
}

// 组内自救撞上组里正在测的一轮(定时测速、点线路时的现测、网络变化时内核自己的全测):要等这一轮测完再判断。
// 那一轮里有一台被墙的(要等超时,这里憋 5 秒),早先只等 4 秒左右就把"还在测"当成"整组不通",跨线路借道到别的线路上 ——
// 其实那一轮测完组自己就换到通的那台了,借过去的却不会自己切回来。
func TestRecoverInGroupWaitsForRoundInFlight(t *testing.T) {
	port, accepted := fakeNodeServer(t, 1, 5*time.Second)
	url := liveURL(t)
	d := lineDaemon(t)
	d.settings.Selected, d.settings.NoExitLookup = "G", true
	d.core.QuietAuto([]string{"dead", "live", "other"}) // 启动那一轮谁都不测:正在测的那一轮由测试来起
	startCore(t, d, `{"type":"selector","tag":"proxy","outbounds":["auto","G","dead","live","other"],"default":"G"},`+
		urltestOut("auto", `"dead","live","other"`, url)+","+urltestOut("G", `"dead","live"`, url)+","+
		`{"type":"socks","tag":"dead","server":"127.0.0.1","server_port":`+port+`},{"type":"direct","tag":"live"},{"type":"direct","tag":"other"}`)
	startRound(t, d, "G", accepted) // 定时测速正好在测 G
	if !d.recoverRoute(context.Background()) {
		t.Fatal("组里有通的那台:应当在组里自救成功")
	}
	if now, _, _ := d.core.Group("proxy"); now != "G" {
		t.Fatalf("撞上正在测的一轮不能当成整组不通、跨线路借道:proxy 落在 %q", now)
	}
	if now := d.groupNow("G"); now != "live" {
		t.Fatalf("组里应换到通的那台: %q", now)
	}
	if s := d.getSettings(); s.Selected != "G" {
		t.Fatalf("自救不改设置: %q", s.Selected)
	}
}

// 整组真不通时,这一组只测一轮就下结论:早先每个空结果都当"可能在测"再重测,一组被完整测三遍
// (每台服务器多挨两次带凭据的探测,丢包的节点每轮要等 15 秒,失效切换白白晚半分钟)。
func TestRecoverDeadGroupTestedOnce(t *testing.T) {
	port, accepted := fakeNodeServer(t, 0, 0)
	url := liveURL(t)
	d := lineDaemon(t)
	d.settings.Selected, d.settings.NoExitLookup = "G", true
	d.core.QuietAuto([]string{"dead1", "dead2", "other"})
	startCore(t, d, `{"type":"selector","tag":"proxy","outbounds":["auto","G","dead1","dead2","other"],"default":"G"},`+
		urltestOut("auto", `"other"`, url)+","+urltestOut("G", `"dead1","dead2"`, url)+","+
		`{"type":"socks","tag":"dead1","server":"127.0.0.1","server_port":`+port+`},`+
		`{"type":"socks","tag":"dead2","server":"127.0.0.1","server_port":`+port+`},{"type":"direct","tag":"other"}`)
	settle(t, d, "G", "auto")
	if n := accepted.Load(); n != 0 {
		t.Fatalf("前提:启动那一轮不该测这两台: %d", n)
	}
	if !d.recoverRoute(context.Background()) {
		t.Fatal("整组不通:应临时借别的线路")
	}
	if now, _, _ := d.core.Group("proxy"); now != "other" {
		t.Fatalf("应借到组外通的那台: %q", now)
	}
	if n := accepted.Load(); n != 2 {
		t.Fatalf("整组不通的那一组只该测一轮(两台各连一次),实际连了 %d 次", n)
	}
}

// 自救测完、准备切 proxy 之前,用户在这期间改选了别的(节点、线路,或关掉「线路自动」固定到某一台):放弃这次切换,
// 不能把用户刚选的盖掉 —— 设置里是新选择,内核却被自救切回旧线路,首页、开关和实际对不上,连接又被掐一次。
func TestRecoverYieldsToUserSelection(t *testing.T) {
	port, accepted := fakeNodeServer(t, 1, 3*time.Second)
	url := liveURL(t)
	d := lineDaemon(t)
	d.settings.Selected, d.settings.NoExitLookup = "G", true
	d.core.QuietAuto([]string{"dead", "live", "other", "us"})
	startCore(t, d, `{"type":"selector","tag":"proxy","outbounds":["auto","G","dead","live","other","us"],"default":"G"},`+
		urltestOut("auto", `"dead","live","other"`, url)+","+urltestOut("G", `"dead","live"`, url)+","+
		`{"type":"socks","tag":"dead","server":"127.0.0.1","server_port":`+port+`},{"type":"direct","tag":"live"},`+
		`{"type":"direct","tag":"other"},{"type":"direct","tag":"us"}`)
	startRound(t, d, "G", accepted)
	done := make(chan bool, 1)
	go func() { done <- d.recoverRoute(context.Background()) }()
	time.Sleep(300 * time.Millisecond) // 自救已经在等这一组测完
	if err := call(t, d, ipc.MSelectNode, map[string]string{"tag": "us"}); err != nil {
		t.Fatal(err)
	}
	if now, _, _ := d.core.Group("proxy"); now != "us" {
		t.Fatalf("前提:用户的选择就地生效: %q", now)
	}
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("自救一直没结束")
	}
	if now, _, _ := d.core.Group("proxy"); now != "us" {
		t.Fatalf("自救不能盖掉用户刚选的:proxy 被切到了 %q", now)
	}
	if s := d.getSettings(); s.Selected != "us" {
		t.Fatalf("设置里是用户的新选择: %q", s.Selected)
	}
}
