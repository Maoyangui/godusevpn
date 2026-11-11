package daemon

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/directnet"
	"github.com/Maoyangui/godusevpn/internal/netmode"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 隧道起来之前的域名解析(节点服务器用域名写的时候要先把它解成地址)。
//
// 以前直接用系统解析器:那是明文 53 发到路由器 / 运营商的 DNS,节点的域名就这么露出去了。
// 防流量逃逸的本意是"隧道以外一个字节都不放",这一步是唯一绕不开的直连 —— 节点自己的地址总得先解出来 ——
// 那就至少用加密的 DoH,和内核给节点域名配的那台(设置里的「本地 DNS」)保持同一台。
// 「本地 DNS」设成 system、或填的是域名 DoH(解析它自己又得先走系统解析器)时,闸开着就干脆不解析
// (节点按域名匹配直连规则,靠嗅探到的 SNI 也能命中),闸没开的模式下才退回系统解析器。

// bootResolver 返回一个按当前设置解析节点域名的函数;返回 nil 表示这一轮不解析。
func (d *Daemon) bootResolver(s settings.Settings) func(ctx context.Context, host string) ([]string, error) {
	s.LocalDNS = builder.LocalDNS(s) // 关着 IPv6 时填的 IPv6 地址不去连,和配置里一致
	sealed := s.NoDirect && s.Mode == settings.ModeGlobal
	if s.LocalDNS == "system" {
		if sealed {
			return nil // 明文系统解析在防逃逸下一个包都不该发
		}
		return func(ctx context.Context, host string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, host)
		}
	}
	if !settings.IsIP(s.LocalDNS) {
		// DoH 服务器自己是域名:解它又得先走系统解析器,防逃逸下同样不做
		if sealed {
			return nil
		}
		return func(ctx context.Context, host string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, host)
		}
	}
	server, allow6 := s.LocalDNS, s.IPv6
	return func(ctx context.Context, host string) ([]string, error) { return lookupDoH(ctx, server, host, allow6) }
}

// bootLookup6 返回一个"这个域名有没有 IPv6 地址"的查询,只给 v6OnlyHosts 分类用;返回 nil 表示不查。
// 只有「本地 DNS」是按地址连的 DoH 时才需要:那时节点域名只问了 A(见 lookupDoH);系统解析器本来就把 IPv6 地址一起给了,
// 不用另外问,防逃逸下不解析的那几种情况(见 bootResolver)也就不问。
func (d *Daemon) bootLookup6(s settings.Settings) func(ctx context.Context, host string) ([]string, error) {
	s.LocalDNS = builder.LocalDNS(s)
	if !settings.IsIP(s.LocalDNS) {
		return nil
	}
	server, allow6 := s.LocalDNS, s.IPv6
	return func(ctx context.Context, host string) ([]string, error) {
		return queryDoH(ctx, server, host, dnsmessage.TypeAAAA, allow6)
	}
}

// bootLookup4 返回一个只问 IPv4 地址的查询,给 v6OnlyHosts 再确认一次"真的没有 A 记录"用:明确答了"没有"才返回 errNoARecord,
// 超时、上游出错都是别的错误(不下结论)。和 bootResolver 用同一台解析器;防逃逸下不解析的那几种情况返回 nil。
func (d *Daemon) bootLookup4(s settings.Settings) func(ctx context.Context, host string) ([]string, error) {
	s.LocalDNS = builder.LocalDNS(s)
	if settings.IsIP(s.LocalDNS) {
		server, allow6 := s.LocalDNS, s.IPv6
		return func(ctx context.Context, host string) ([]string, error) { return lookupDoH(ctx, server, host, allow6) }
	}
	if s.NoDirect && s.Mode == settings.ModeGlobal {
		return nil // 同 bootResolver:明文系统解析在防逃逸下一个包都不该发
	}
	return func(ctx context.Context, host string) ([]string, error) {
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
		var de *net.DNSError
		if errors.As(err, &de) && de.IsNotFound {
			return nil, errNoARecord
		}
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(ips))
		for _, ip := range ips {
			out = append(out, ip.String())
		}
		if len(out) == 0 {
			return nil, errNoARecord
		}
		return out, nil
	}
}

// v6OnlyHosts 用域名写的节点里,只解析得到 IPv6 地址的那些(域名 → true)。关着 IPv6 时内核给节点域名解析按 ipv4_only 来
// (route.default_domain_resolver 只写了服务器,策略跟着 dns.strategy),这种节点解析不出地址、一定连不上;找出来交给配置生成
// 从自动选择里摘掉、界面标成「IPv6 节点」并说清楚原因(见 builder.V6Blocked),而不是放着它一直报"当前节点不可用"。
//
// ips、noA 是 resolveNodeHosts 的结果:系统解析器一并给出 IPv6 地址,全是 IPv6 的是候选;DoH 只问了 A,问到了却没有 A 记录的(noA)
// 是候选。候选要**再确认一次**才下结论:lookup4 再单问一次 IPv4,明确答"没有 A 记录"(errNoARecord)才算 —— 上游偶发一次空答复、
// 系统解析器 A 查询超时只剩 AAAA(Go 的解析器默认不严格,网络一抖双栈域名就只剩 IPv6 地址),都不能把能用的节点判成连不上;
// DoH 那一路再用 lookup6 问一次 AAAA,确实有 IPv6 地址才算。问的是同一台解析器、同一个域名,不多露任何东西,只用来分类,不拿它去连。
// 确认不了(lookup4 为 nil、超时、出错)就不下结论。连不上 DoH 的那种本来就不是候选:不然 DoH 不通时每次连接都要多等一轮超时。
func v6OnlyHosts(ctx context.Context, p *profile.Profile, ips map[string][]string, noA map[string]bool, lookup4, lookup6 func(context.Context, string) ([]string, error)) map[string]bool {
	if p == nil {
		return nil
	}
	type cand struct {
		host string
		ask6 bool // DoH 那一路:还要问 AAAA
	}
	var cands []cand
	seen := map[string]bool{}
	for _, hp := range p.Servers() {
		h := hp
		if i := strings.LastIndex(h, ":"); i > 0 {
			h = h[:i]
		}
		if h == "" || settings.IsIP(h) || seen[h] {
			continue
		}
		seen[h] = true
		if addrs, ok := ips[h]; ok {
			if allV6(addrs) {
				cands = append(cands, cand{host: h})
			}
			continue
		}
		if noA[h] {
			cands = append(cands, cand{host: h, ask6: true})
		}
	}
	if len(cands) == 0 || lookup4 == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	out := map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, c := range cands {
		if c.ask6 && lookup6 == nil {
			continue
		}
		wg.Add(1)
		go func(c cand) {
			defer wg.Done()
			if _, err := lookup4(ctx, c.host); !errors.Is(err, errNoARecord) {
				return // 再问一次有 A 了,或者这次没问出结果:不下结论
			}
			if c.ask6 {
				if addrs, err := lookup6(ctx, c.host); err != nil || len(addrs) == 0 {
					return
				}
			}
			mu.Lock()
			out[c.host] = true
			mu.Unlock()
		}(c)
	}
	wg.Wait()
	if len(out) == 0 {
		return nil
	}
	return out
}

// allV6 解析结果里全是 IPv6 地址(至少一个)。
func allV6(addrs []string) bool {
	if len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		ip, err := netip.ParseAddr(strings.TrimSpace(a))
		if err != nil || !ip.Is6() || ip.Is4In6() {
			return false
		}
	}
	return true
}

// errNoARecord 解析器明确答了:这个域名没有 A 记录(和连不上、上游出错分开:前者才值得再问一次 AAAA,见 v6OnlyHosts)。
var errNoARecord = errors.New("没有 A 记录")

// lookupDoH RFC 8484:POST application/dns-message 到 https://<ip>/dns-query,只查 A(IPv6 全程禁用)。
// 服务器按地址连,证书按地址校验(223.5.5.5、1.1.1.1 这些公共 DoH 的证书都带地址),不经任何代理。
// 答复是 NOERROR 而里面没有 A 记录(NODATA)才返回 errNoARecord;SERVFAIL、REFUSED、NXDOMAIN 是别的错(见 parseDoHAnswer)。
// allow6 是「允许 IPv6」:关着时 DoH 服务器自己写成 IPv6 地址也不去连(见 queryDoH)。
func lookupDoH(ctx context.Context, server, host string, allow6 bool) ([]string, error) {
	addrs, err := queryDoH(ctx, server, host, dnsmessage.TypeA, allow6)
	if err == nil && len(addrs) == 0 {
		return nil, errNoARecord
	}
	return addrs, err
}

// queryDoH 问一种记录(A 或 AAAA),返回地址;答复是 NOERROR 而没有这种记录时返回空列表。
// 问 AAAA 只是问,不拿它去连;连 DoH 服务器本身在关着 IPv6(allow6 为假)时只拨 IPv4 —— 服务器写成 IPv6 地址就不去连。
func queryDoH(ctx context.Context, server, host string, qtype dnsmessage.Type, allow6 bool) ([]string, error) {
	name, err := dnsmessage.NewName(host + ".")
	if err != nil {
		return nil, err
	}
	msg := dnsmessage.Message{
		Header:    dnsmessage.Header{RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: name, Type: qtype, Class: dnsmessage.ClassINET}},
	}
	packed, err := msg.Pack()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+net.JoinHostPort(server, "443")+"/dns-query", bytes.NewReader(packed))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	tr := &http.Transport{
		DialContext:       directnet.Dial((&net.Dialer{Timeout: 4 * time.Second, Control: netmode.SelfControl}).DialContext, func() bool { return allow6 }), // Linux 的闸按标记放行本服务
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		Proxy:             nil,  // 绝不走环境变量里的代理
		DisableKeepAlives: true, // 一问一答就完:不留一条空闲连接在隧道外挂着发 keepalive
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Timeout: 4 * time.Second, Transport: tr}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("DoH %s: HTTP %d", server, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	return parseDoHAnswer(body, qtype)
}

// parseDoHAnswer 解 DoH 的答复,取出 qtype 那种记录的地址。RFC 8484 下 SERVFAIL、REFUSED 也是 HTTP 200 回来的:
// RCODE 不是 NOERROR 一律算错(上游出错 / 域名不存在),不能当成"这个域名没有这种记录"。
func parseDoHAnswer(body []byte, qtype dnsmessage.Type) ([]string, error) {
	var out dnsmessage.Message
	if err := out.Unpack(body); err != nil {
		return nil, err
	}
	if out.Header.RCode != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("DoH 答复 %v", out.Header.RCode)
	}
	var addrs []string
	for _, a := range out.Answers {
		switch r := a.Body.(type) {
		case *dnsmessage.AResource:
			if qtype == dnsmessage.TypeA {
				addrs = append(addrs, net.IP(r.A[:]).String())
			}
		case *dnsmessage.AAAAResource:
			if qtype == dnsmessage.TypeAAAA {
				addrs = append(addrs, net.IP(r.AAAA[:]).String())
			}
		}
	}
	return addrs, nil
}
