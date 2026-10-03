package builder

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/experimental/clashmode"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	R "github.com/sagernet/sing-box/route/rule"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/service"
)

// 只看规则的形状证明不了"会走哪条":sing-box 的规则项各有各的判法(ip_is_private 对域名看解析结果、
// ip_cidr 是"任一地址命中"、logical 的 invert……)。这里用 sing-box 自己的规则引擎把生成的路由规则
// 从上往下过一遍,和内核 matchRule 同一个顺序。

// clashModeCtx 给 clash_mode 规则项一个"当前模式"(1.14.2 起由 clashmode.Manager 提供)。
func clashModeCtx(mode string) context.Context {
	ctx := context.Background()
	return service.ContextWithPtr(ctx, clashmode.NewManager(ctx, log.NewNOPFactory().Logger(), mode, []string{"Rule", "Global", "Direct"}))
}

// conn 一条要路由的连接。
type conn struct {
	dst     string   // IP 字面量或域名(fake-ip 还原出来的那种)
	port    uint16   // 默认 443
	src     string   // 来源地址(网关模式的设备);空 = 本机
	proc    string   // 发起进程的完整路径(桌面)
	answers []string // 目的地是域名时 resolve 拿到的地址
}

// verdict 路由结果。
type verdict struct {
	out      string       // 出口;"reject" = 拒绝;都没命中是 final
	at       int          // 命中规则的下标;-1 = 落到 final
	resolved []netip.Addr // 交给出口时的目的地址(resolve 之后)
	server   string       // resolve 指定的 DNS 服务器;空 = 按 DNS 规则
}

func route(t *testing.T, c cfg, mode string, cn conn) (string, int) {
	t.Helper()
	v := routeV(t, c, mode, cn)
	return v.out, v.at
}

// routeV 引用规则集的规则跳过(测试里的目的地都不在规则集里);resolve 动作用 answers 模拟解析结果。
func routeV(t *testing.T, c cfg, mode string, cn conn) verdict {
	t.Helper()
	var server string
	ctx := clashModeCtx(mode)
	md := adapter.InboundContext{Network: "tcp"}
	port := cn.port
	if port == 0 {
		port = 443
	}
	if ip, err := netip.ParseAddr(cn.dst); err == nil {
		md.Destination = M.SocksaddrFrom(ip, port)
		md.IPVersion = 4
		if ip.Is6() {
			md.IPVersion = 6
		}
	} else {
		md.Destination = M.Socksaddr{Fqdn: cn.dst, Port: port}
	}
	if cn.src != "" {
		md.Source = M.SocksaddrFrom(netip.MustParseAddr(cn.src), 50000)
	}
	if cn.proc != "" {
		md.ProcessInfo = &adapter.ConnectionOwner{ProcessPath: cn.proc}
	}
	logger := log.NewNOPFactory().Logger()
	for i, raw := range c.Route.Rules {
		b, _ := json.Marshal(raw)
		if strings.Contains(string(b), `"rule_set"`) {
			continue
		}
		var opt option.Rule
		if err := opt.UnmarshalJSONContext(ctx, b); err != nil {
			t.Fatalf("规则 %d 解不开: %v\n%s", i, err, b)
		}
		r, err := R.NewRule(ctx, logger, opt, false)
		if err != nil {
			t.Fatalf("规则 %d 建不起来: %v\n%s", i, err, b)
		}
		if err := r.Start(); err != nil {
			t.Fatal(err)
		}
		md.ResetRuleCache()
		matched := r.Match(&md)
		_ = r.Close()
		if !matched {
			continue
		}
		switch a := r.Action().(type) {
		case *R.RuleActionSniff:
		case *R.RuleActionHijackDNS:
			return verdict{"hijack-dns", i, nil, server}
		case *R.RuleActionResolve:
			if md.Destination.IsFqdn() {
				server = a.Server
				md.DestinationAddresses = nil
				for _, s := range cn.answers {
					ip := netip.MustParseAddr(s)
					if a.Strategy == C.DomainStrategyIPv4Only && !ip.Is4() {
						continue
					}
					md.DestinationAddresses = append(md.DestinationAddresses, ip)
				}
			}
		case *R.RuleActionReject:
			return verdict{"reject", i, md.DestinationAddresses, server}
		case *R.RuleActionRoute:
			return verdict{a.Outbound, i, md.DestinationAddresses, server}
		default:
			t.Fatalf("规则 %d 的动作没料到: %T", i, a)
		}
	}
	return verdict{c.Route.Final, -1, md.DestinationAddresses, server}
}
