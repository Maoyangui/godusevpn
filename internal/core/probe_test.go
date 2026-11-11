package core

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing/service"
)

// 内核没跑时的测速:只用出站起临时实例;连不上的节点要在父上下文超时内给出 -1,不能挂死也不能 panic。
func TestProbeOfflineUnreachable(t *testing.T) {
	outbounds := []json.RawMessage{
		json.RawMessage(`{"type":"hysteria2","tag":"hk","server":"192.0.2.1","server_port":443,"password":"p","tls":{"enabled":true,"server_name":"a.example"}}`),
		json.RawMessage(`{"type":"anytls","tag":"tw","server":"192.0.2.2","server_port":8443,"password":"p","tls":{"enabled":true,"server_name":"b.example"}}`),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	res, err := Probe(ctx, outbounds, []string{"hk", "tw", "不存在"}, "", "", true, nil)
	if err != nil {
		t.Fatalf("这几个节点内核都解得动,不该报错: %v", err)
	}
	if time.Since(start) > 6*time.Second {
		t.Fatal("超时后应尽快返回")
	}
	if len(res) != 2 || res["hk"] != -1 || res["tw"] != -1 {
		t.Fatalf("连不上的节点应为 -1,不存在的节点不出现: %v", res)
	}
}

// 临时测速实例绝不能往进程当前目录写文件。
// sing-box 的 needCacheFile 是 `CacheFile.Enabled || PlatformLogWriter != nil`,而这份临时配置里
// 没有 experimental 段 —— 只要传了 PlatformLogWriter,缓存文件就会落到默认路径 "cache.db",
// 那是相对**进程当前目录**的。Android 上当前目录是 /,写不进去,box.Start() 直接失败,
// 未连接时测速一个数都出不来,而且是完全静默的;Windows 上服务以 SYSTEM 跑,会往 System32 里丢文件。
func TestProbeWritesNothingToWorkingDir(t *testing.T) {
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = Probe(ctx, []json.RawMessage{
		json.RawMessage(`{"type":"anytls","tag":"tw","server":"192.0.2.2","server_port":8443,"password":"p","tls":{"enabled":true,"server_name":"b.example"}}`),
	}, []string{"tw"}, "", "", false, nil)

	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		t.Fatalf("测速在当前目录留下了 %s —— Android 上这一步会直接失败,所有节点都测不出来", e.Name())
	}
}

// 订阅里有内核解不动的节点时,不能把整轮测速一起拖垮:坏的那个报 -1,别的照测。
func TestProbeSkipsUnparsableOutbound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	res, err := Probe(ctx, []json.RawMessage{
		json.RawMessage(`{"type":"anytls","tag":"好的","server":"192.0.2.2","server_port":8443,"password":"p","tls":{"enabled":true,"server_name":"b.example"}}`),
		json.RawMessage(`{"type":"这个类型内核不认识","tag":"坏的","server":"192.0.2.3","server_port":443}`),
	}, []string{"好的", "坏的"}, "", "", false, nil)
	if err == nil {
		t.Fatal("跳过了坏节点,应该把这件事报上来")
	}
	if _, ok := res["好的"]; !ok {
		t.Fatalf("好的节点应该照测出来: %v", res)
	}
	if res["坏的"] != -1 {
		t.Fatalf("解不动的节点该报 -1,而不是在界面上留白让人以为没测过: %v", res)
	}
}

// 手动选节点时连接不碰别的节点:自动选择组启动时那一轮被预先记的"刚测过"跳过,历史里延迟是 0(界面上的"没测过")。
func TestQuietAutoSkipsFirstRound(t *testing.T) {
	c := New(nil)
	c.QuietAuto([]string{"a", "b"})
	cfg := `{"log":{"disabled":true},"outbounds":[
	  {"type":"urltest","tag":"auto","outbounds":["a","b"],"url":"http://127.0.0.1:1/","interval":"24h","idle_timeout":"25h"},
	  {"type":"socks","tag":"a","server":"127.0.0.1","server_port":1},{"type":"socks","tag":"b","server":"127.0.0.1","server_port":1},
	  {"type":"direct","tag":"direct"}]}`
	if err := c.Start([]byte(cfg)); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	time.Sleep(500 * time.Millisecond) // 真测了的话,连不上的节点这时候历史已经被删掉了
	_, ctx, _ := c.snapshot()
	h := service.PtrFromContext[urltest.HistoryStorage](ctx)
	for _, tag := range []string{"a", "b"} {
		v := h.LoadURLTestHistory(tag)
		if v == nil || v.Delay != 0 {
			t.Fatalf("%s: %+v", tag, v)
		}
	}
	if took, err := c.DialDirect(context.Background(), "127.0.0.1", 1); err == nil {
		t.Fatalf("没人听的端口不该连得上: %v", took)
	}
}

// 带中转的节点(detour)没连接时也得测得出来:临时实例里要连它链上的中转一起放 —— 只放它自己,内核找不到中转,
// 整个实例起不来,这一批一个数都没有。链断了(指向订阅里没有的出站)、成环的只有它自己报 -1,别的照测;中转只是放进去拨号用,不单独测。
func TestProbeChainedNodeViaRelay(t *testing.T) {
	relay, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	var greetings atomic.Int32
	go func() {
		for {
			c, err := relay.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
				b := make([]byte, 1)
				if n, _ := c.Read(b); n == 1 && b[0] == 0x05 { // 经它拨出去的才会发 SOCKS 握手;收到就关,链测出来是不通
					greetings.Add(1)
				}
			}()
		}
	}()
	port := strconv.Itoa(relay.Addr().(*net.TCPAddr).Port)
	outbounds := []json.RawMessage{
		json.RawMessage(`{"type":"socks","tag":"中转","server":"127.0.0.1","server_port":` + port + `}`),
		json.RawMessage(`{"type":"socks","tag":"落地","server":"2001:db8::1","server_port":1080,"detour":"中转"}`),
		json.RawMessage(`{"type":"socks","tag":"两跳","server":"192.0.2.9","server_port":1080,"detour":"落地"}`),
		json.RawMessage(`{"type":"socks","tag":"断链","server":"192.0.2.10","server_port":1080,"detour":"没有这个"}`),
		json.RawMessage(`{"type":"socks","tag":"环A","server":"192.0.2.11","server_port":1080,"detour":"环B"}`),
		json.RawMessage(`{"type":"socks","tag":"环B","server":"192.0.2.12","server_port":1080,"detour":"环A"}`),
		json.RawMessage(`{"type":"socks","tag":"坏中转的","server":"192.0.2.13","server_port":1080,"detour":"坏的"}`),
		json.RawMessage(`{"type":"这个类型内核不认识","tag":"坏的","server":"192.0.2.14","server_port":443}`),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := Probe(ctx, outbounds, []string{"落地", "两跳", "断链", "环A", "坏中转的"}, "http://example.invalid/", "", false, nil)
	if err == nil || !strings.Contains(err.Error(), "断链") || !strings.Contains(err.Error(), "环A") || !strings.Contains(err.Error(), "坏中转的") {
		t.Fatalf("中转接不上的几个要报上来: %v", err)
	}
	for deadline := time.Now().Add(3 * time.Second); greetings.Load() < 2 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if greetings.Load() < 2 {
		t.Fatalf("两条链(一跳、两跳)都该经中转拨出去: 中转收到 %d 次握手,结果 %v", greetings.Load(), res)
	}
	for _, tag := range []string{"落地", "两跳", "断链", "环A", "坏中转的"} {
		if v, ok := res[tag]; !ok || v != -1 {
			t.Fatalf("%s 该有结果(中转一握手就关,测出来是不通;接不上中转的直接 -1): %v", tag, res)
		}
	}
	if _, ok := res["中转"]; ok {
		t.Fatalf("中转只是放进去给链拨号用,没要测它就不出结果: %v", res)
	}
}
