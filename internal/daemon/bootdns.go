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
	server := s.LocalDNS
	return func(ctx context.Context, host string) ([]string, error) { return lookupDoH(ctx, server, host) }
}

// bootLookup6 返回一个"这个域名有没有 IPv6 地址"的查询,只给 v6OnlyHosts 分类用;返回 nil 表示不查。
// 只有「本地 DNS」是按地址连的 DoH 时才需要:那时节点域名只问了 A(见 lookupDoH);系统解析器本来就把 IPv6 地址一起给了,
// 不用另外问,防逃逸下不解析的那几种情况(见 bootResolver)也就不问。
func (d *Daemon) bootLookup6(s settings.Settings) func(ctx context.Context, host string) ([]string, error) {
	if !settings.IsIP(s.LocalDNS) {
		return nil
	}
	server := s.LocalDNS
	return func(ctx context.Context, host string) ([]string, error) {
		return queryDoH(ctx, server, host, dnsmessage.TypeAAAA)
	}
}

// v6OnlyHosts 用域名写的节点里,只解析得到 IPv6 地址的那些(域名 → true)。关着 IPv6 时内核给节点域名解析按 ipv4_only 来
// (route.default_domain_resolver 只写了服务器,策略跟着 dns.strategy),这种节点解析不出地址、一定连不上;找出来交给配置生成
// 从自动选择里摘掉、界面标成「IPv6 节点」并说清楚原因(见 builder.V6Blocked),而不是放着它一直报"当前节点不可用"。
// ips、noA 是 resolveNodeHosts 的结果:系统解析器一并给出 IPv6 地址,全是 IPv6 的直接算;DoH 只问了 A,问到了却没有 A 记录的(noA)
// 再用 lookup6 问一次 AAAA —— 问的是同一台 DoH、同一个域名,不多露任何东西,而且只用来分类,不拿它去连。
// 连不上 DoH 的那种不补问:不然 DoH 不通时每次连接都要多等一轮超时。lookup6 为 nil 时只看 ips。
func v6OnlyHosts(ctx context.Context, p *profile.Profile, ips map[string][]string, noA map[string]bool, lookup6 func(context.Context, string) ([]string, error)) map[string]bool {
	if p == nil {
		return nil
	}
	out := map[string]bool{}
	var ask []string
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
				out[h] = true
			}
			continue
		}
		if noA[h] {
			ask = append(ask, h)
		}
	}
	if len(ask) > 0 && lookup6 != nil {
		ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, h := range ask {
			wg.Add(1)
			go func(h string) {
				defer wg.Done()
				if addrs, err := lookup6(ctx, h); err == nil && len(addrs) > 0 {
					mu.Lock()
					out[h] = true
					mu.Unlock()
				}
			}(h)
		}
		wg.Wait()
	}
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

// errNoARecord DoH 答了,但这个域名没有 A 记录(和连不上 DoH 分开:前者才值得再问一次 AAAA,见 v6OnlyHosts)。
var errNoARecord = errors.New("没有 A 记录")

// lookupDoH RFC 8484:POST application/dns-message 到 https://<ip>/dns-query,只查 A(IPv6 全程禁用)。
// 服务器按地址连,证书按地址校验(223.5.5.5、1.1.1.1 这些公共 DoH 的证书都带地址),不经任何代理。
func lookupDoH(ctx context.Context, server, host string) ([]string, error) {
	addrs, err := queryDoH(ctx, server, host, dnsmessage.TypeA)
	if err == nil && len(addrs) == 0 {
		return nil, errNoARecord
	}
	return addrs, err
}

// queryDoH 问一种记录(A 或 AAAA),返回地址;没有这种记录时返回空列表。
func queryDoH(ctx context.Context, server, host string, qtype dnsmessage.Type) ([]string, error) {
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
		DialContext:       (&net.Dialer{Timeout: 4 * time.Second, Control: netmode.SelfControl}).DialContext, // Linux 的闸按标记放行本服务
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
	var out dnsmessage.Message
	if err := out.Unpack(body); err != nil {
		return nil, err
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
