package core

import (
	"context"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Maoyangui/godusevpn/internal/profile"
)

// dualListen IPv4、IPv6 回环上各听同一个端口(双栈域名解析成哪一边,就连到哪一边);没有 IPv6 回环就跳过。
func dualListen(t *testing.T) (ln4, ln6 net.Listener, port string) {
	t.Helper()
	probe, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("这台机器没有 IPv6 回环")
	}
	_ = probe.Close()
	for i := 0; i < 20; i++ {
		ln4, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		p := ln4.Addr().(*net.TCPAddr).Port
		if ln6, err := net.Listen("tcp6", "[::1]:"+strconv.Itoa(p)); err == nil {
			t.Cleanup(func() { _ = ln4.Close(); _ = ln6.Close() })
			return ln4, ln6, strconv.Itoa(p)
		}
		_ = ln4.Close()
	}
	t.Skip("凑不出 IPv4、IPv6 回环上同一个空闲端口")
	return nil, nil, ""
}

func countConns(ln net.Listener, n *atomic.Int32) {
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			_ = c.Close()
		}
	}()
}

// 订阅节点上遗留的 domain_strategy(sing-box 已废弃的写法)会盖过客户端给节点域名定的解析策略 —— sing-box 的 dialer 在出站写了它、
// 又没写 domain_resolver 时拿它当解析策略:关着 IPv6(dns.strategy = ipv4_only)时写了 ipv6_only 的节点照样解析 AAAA、经 IPv6 去连。
// 订阅读进来时去掉了它(profile.Parse;旧缓存由 profile.Load 去掉),同一个双栈域名的节点就只连 IPv4。
// dual.test 由 hosts 解析成 ::1 和 127.0.0.1(和真连时一样:route.default_domain_resolver 只写服务器,策略跟着 dns.strategy)。
func TestSubscriptionDomainStrategyCannotOverrideIPv4Only(t *testing.T) {
	ln4, ln6, port := dualListen(t)
	var hits4, hits6 atomic.Int32
	countConns(ln4, &hits4)
	countConns(ln6, &hits6)
	node := `{"type":"socks","tag":"偏六","server":"dual.test","server_port":` + port + `,"domain_strategy":"ipv6_only"}`
	p, err := profile.Parse([]byte(`{"outbounds":[`+node+`]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"type":"socks","tag":"原样","server":"dual.test","server_port":` + port + `,"domain_strategy":"ipv6_only"}` // 没经过订阅清洗的(对照)
	cfg := `{"log":{"level":"error"},
	  "dns":{"servers":[{"type":"hosts","tag":"hosts","predefined":{"dual.test":["::1","127.0.0.1"]}}],"strategy":"ipv4_only"},
	  "outbounds":[` + string(p.Outbounds[0]) + `,` + raw + `,{"type":"direct","tag":"direct"}],
	  "route":{"default_domain_resolver":"hosts","final":"direct"}}`
	c := New(nil)
	if err := c.Start([]byte(cfg)); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	wait := func(n *atomic.Int32, want int32) {
		for deadline := time.Now().Add(3 * time.Second); n.Load() < want && time.Now().Before(deadline); {
			time.Sleep(20 * time.Millisecond)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_, _ = c.URLTest(ctx, "偏六", "http://example.invalid/") // 握手就被关掉,测出来是不通;要看的是连到了哪一边
	wait(&hits4, 1)
	if hits6.Load() != 0 || hits4.Load() < 1 {
		t.Fatalf("订阅里写了 ipv6_only 的节点,关着 IPv6 时也只能经 IPv4 去连: IPv4 %d 次、IPv6 %d 次", hits4.Load(), hits6.Load())
	}
	// 对照:没经过清洗、还带着 domain_strategy 的节点真会经 IPv6 去连 —— 上面防的就是它。哪天 sing-box 不再认这个字段,对照不成立,只记一笔
	_, _ = c.URLTest(ctx, "原样", "http://example.invalid/")
	wait(&hits6, 1)
	if hits6.Load() == 0 {
		t.Logf("对照没有经 IPv6 去连:这版 sing-box 已经不按出站的 domain_strategy 改解析策略了")
	}
}
