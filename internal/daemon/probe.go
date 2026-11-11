package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/core"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 未连接时的测速:不经任何出站,直接量本机到节点服务器的往返。
// TCP 类协议(vmess / vless / trojan / shadowsocks / anytls …)量 TCP 握手时间;
// UDP 类协议(hysteria2 / tuic / wireguard …)服务器不听 TCP,量 ICMP ping(服务以 SYSTEM 跑,能开原始套接字);
// ping 不通(服务器或用户所在网络拦了 ICMP、本机开不了原始套接字)的节点退回"临时实例经出站真连一次",
// 不直接判"不通"。带中转的节点(detour)一律经临时实例真连一次,不从本机直接碰落地(见 endpoint.chained)。
// 已连接时另有一套(见 probeConnected)。

const probeTimeout = 4 * time.Second

// idleBytes 两轮定时测速之间内核走过的流量少于这么多,就算没人在用(系统自己的零星连接不算)。
const idleBytes = 256 << 10

// probeBudget 整轮测速的总时限。并发 8、直连每个最多 4 秒、退回临时实例的再最多 8 秒:
// 节点多的时候固定 40 秒根本轮不完,排在后面的会因为总闹钟到了被记成"不通"。按节点数算,再留 10 秒余量,封顶三分钟。
func probeBudget(n int) time.Duration {
	d := time.Duration((n+7)/8)*(probeTimeout+8*time.Second) + 10*time.Second
	if d > 3*time.Minute {
		d = 3 * time.Minute
	}
	return d
}

var udpProtocols = map[string]bool{"hysteria": true, "hysteria2": true, "tuic": true, "wireguard": true}

type endpoint struct {
	Type   string `json:"type"`
	Tag    string `json:"tag"`
	Server string `json:"server"`
	Port   int    `json:"server_port"`
	Detour string `json:"detour"`
}

// chained 带中转的节点(sing-box 的 detour):本机只连中转,落地那一跳由中转服务器去连、去解析(见 builder.V6Blocked)。
// 测它不能从本机直接握手 / ping 落地:那量的不是这条链的延迟,落地服务器还会直接看到本机的地址 —— 用中转往往正是为了不让它看到;
// 落地写成 IPv6 时那就是从本机走 IPv6,关着 IPv6 也一样。所以和 UDP 类一样经它自己的出站真连一次(经中转)。
// detour 是 direct 的就是本机直连,照常测。
func (e endpoint) chained() bool { return e.Detour != "" && e.Detour != "direct" }

func endpoints(p *profile.Profile) []endpoint {
	out := make([]endpoint, 0, len(p.Outbounds))
	for _, raw := range p.Outbounds {
		var e endpoint
		if json.Unmarshal(raw, &e) == nil && e.Tag != "" {
			out = append(out, e)
		}
	}
	return out
}

// probeDirect 返回 节点 → 毫秒,-1 = 不通。
// onEach 不为空时每测出一个就先报一次,界面好一个一个显示,不用干等全部测完。
//
// resolve 解析节点域名(见 bootResolver),这时候隧道还没起来:交给系统解析器就是把节点域名明文发给运营商,
// 而真连节点时域名是经 DoH 解析的,连接里也不一定看得到它。localDNS 给退回临时实例的那条路用,是地址时临时实例也走 DoH。
// allow6 是「允许 IPv6」:关着时一个包都不往 IPv6 地址发(解析只取 IPv4、拨号只用 IPv4,临时实例按 ipv4_only 解析);
// skip 是这一轮不用的 IPv6 节点(见 builder.V6Blocked):不测,直接记不通 —— 测它就是在走 IPv6。
// 带中转的节点(见 endpoint.chained)不从本机直接碰落地,直接交给临时实例经中转真连一次。
func probeDirect(ctx context.Context, p *profile.Profile, onEach func(tag string, ms int), logf func(string, ...any), resolve func(context.Context, string) ([]string, error), localDNS string, allow6 bool, skip map[string]bool) map[string]int {
	res := map[string]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	var fallback []string // ping 没通的 UDP 节点、带中转的节点,稍后用临时实例经它们自己的出站真连一次
	for _, e := range endpoints(p) {
		if e.Server == "" || e.Port <= 0 || skip[e.Tag] {
			mu.Lock()
			res[e.Tag] = -1
			mu.Unlock()
			if onEach != nil {
				onEach(e.Tag, -1)
			}
			continue
		}
		if e.chained() {
			mu.Lock() // 前面起的那几路 ping 不通时也在往 fallback 里加
			fallback = append(fallback, e.Tag)
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(e endpoint) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return // 被取消了(比如闸开了):还没开始的一个都不再发 —— 解析一开头就会发出查询,不能只靠超时
			}
			tctx, cancel := context.WithTimeout(ctx, probeTimeout)
			defer cancel()
			var d time.Duration
			var err error
			if udpProtocols[e.Type] {
				d, err = pingHost(tctx, e.Server, resolve, allow6)
				if err != nil {
					// ping 不通不等于节点不通:服务器防火墙丢 ICMP、用户所在网络(校园网、公司网)不放行 ICMP
					// 都很常见,本机开不了原始套接字也走这里。这些节点改用临时实例真连一次再下结论。
					mu.Lock()
					fallback = append(fallback, e.Tag)
					mu.Unlock()
					return
				}
			} else {
				d, err = tcping(tctx, e.Server, strconv.Itoa(e.Port), resolve, allow6)
			}
			ms := int(d / time.Millisecond)
			if ms < 1 {
				ms = 1 // 计时器粒度粗时会量出 0,而 0 在界面上表示"没测过"
			}
			v := ms
			if err != nil {
				v = -1
			}
			mu.Lock()
			res[e.Tag] = v
			mu.Unlock()
			if onEach != nil {
				onEach(e.Tag, v)
			}
		}(e)
	}
	wg.Wait()
	if len(fallback) > 0 && ctx.Err() == nil {
		doh := ""
		if settings.IsIP(localDNS) {
			doh = localDNS
		}
		got, err := core.Probe(ctx, p.Outbounds, fallback, "", doh, allow6, onEach)
		if err != nil {
			// 以前这里失败是完全静默的:界面上那批节点永远没有数字,用户看到的是"点了没反应"。
			if logf != nil {
				logf("未连接时测速:%v", err)
			}
		}
		for k, v := range got {
			res[k] = v
		}
	}
	return res
}

// lookupNode 节点域名 → 地址,IPv4 优先(和内核解析节点域名的 prefer_ipv4 / ipv4_only 一样):系统解析器可能把 IPv6 地址排在前面。
// 关着 IPv6(allow6 为假)时只要 IPv4,只有 IPv6 地址就报错、不去连;本来就写成 IPv6 地址的同样报错、不去连 —— 连着时拨号用的是
// 内核的直连出站,不像未连接时的 tcping 另有 tcp4 兜着,得在这一步挡住。resolve 为空时域名原样返回,由拨号那一步解析
// (关着 IPv6 时 tcping 只拨 IPv4,内核的直连出站按 ipv4_only 解析)。
func lookupNode(ctx context.Context, host string, resolve func(context.Context, string) ([]string, error), allow6 bool) (string, error) {
	if ip, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(host), "["), "]")); err == nil {
		if ip = ip.Unmap(); ip.Is6() && !allow6 {
			return "", errors.New("地址是 IPv6,而「允许 IPv6」关着")
		}
		return ip.String(), nil
	}
	if resolve == nil {
		return host, nil
	}
	ips, err := resolve(ctx, host)
	if err != nil {
		return "", err
	}
	v6 := ""
	for _, a := range ips {
		ip := net.ParseIP(strings.TrimSpace(a))
		switch {
		case ip == nil:
		case ip.To4() != nil:
			return ip.String(), nil
		case v6 == "":
			v6 = ip.String()
		}
	}
	if v6 != "" && allow6 {
		return v6, nil
	}
	if v6 != "" {
		return "", errors.New("只有 IPv6 地址,而「允许 IPv6」关着")
	}
	return "", errors.New("解析失败")
}

// probeConnected 连着时给节点列表测速:TCP 类协议直接量网卡到节点的握手,不经节点转发。线路上看到的和真连一样只有地址和端口,
// 少了一轮代理握手和一条经节点发出去的请求,数字也正是这条线路本身的延迟。只测得出"连得上",账号过期这种要选上之后才看得出。
// UDP 类协议(hysteria2、tuic)没有 TCP 可握,ICMP 从这里发又会进隧道,只能经出站真连一次。带中转的节点(见 endpoint.chained)
// 也经它自己的出站真连一次:从本机直接握手落地,量的不是这条链,落地还会直接看到本机的地址(写成 IPv6 的就是从本机走 IPv6)。
//
// 关着 IPv6 时同样不走 IPv6:skip(这一轮不用的 IPv6 节点)不测、直接记不通,解析出来的地址只用 IPv4 的,写成 IPv6 地址的不拨(见 lookupNode)。
func (d *Daemon) probeConnected(ctx context.Context, p *profile.Profile, onEach func(tag string, ms int), skip map[string]bool) map[string]int {
	s := d.getSettings()
	resolve := d.bootResolver(s)
	res := map[string]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	var viaOut []string // 经出站真连一次的:UDP 类、带中转的
	for _, e := range endpoints(p) {
		if skip[e.Tag] {
			mu.Lock() // 前面起的那几路已经在往 res 里写了
			res[e.Tag] = -1
			mu.Unlock()
			if onEach != nil {
				onEach(e.Tag, -1)
			}
			continue
		}
		if udpProtocols[e.Type] || e.chained() {
			viaOut = append(viaOut, e.Tag)
			continue
		}
		if e.Server == "" || e.Port <= 0 {
			continue
		}
		wg.Add(1)
		go func(e endpoint) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			tctx, cancel := context.WithTimeout(ctx, probeTimeout)
			defer cancel()
			v := -1
			if host, err := lookupNode(tctx, e.Server, resolve, s.IPv6); err == nil {
				if took, err := d.core.DialDirect(tctx, host, e.Port); err == nil {
					if v = int(took / time.Millisecond); v < 1 {
						v = 1
					}
				}
			}
			mu.Lock()
			res[e.Tag] = v
			mu.Unlock()
			if onEach != nil {
				onEach(e.Tag, v)
			}
		}(e)
	}
	wg.Wait()
	if len(viaOut) > 0 {
		for tag, v := range d.core.ProbeRunning(ctx, viaOut, builder.TestURL, onEach) {
			res[tag] = v
		}
	}
	return res
}

func tcping(ctx context.Context, host, port string, resolve func(context.Context, string) ([]string, error), allow6 bool) (time.Duration, error) {
	host, err := lookupNode(ctx, host, resolve, allow6)
	if err != nil {
		return 0, err
	}
	addr := net.JoinHostPort(host, port)
	network := "tcp"
	if !allow6 {
		network = "tcp4" // 关着 IPv6:域名交给系统解析时也只拨 IPv4,写成 IPv6 地址的直接拨不出去
	}
	start := time.Now()
	c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return 0, err
	}
	c.Close()
	return time.Since(start), nil
}

// icmpUnavailable 原始套接字开不了(没权限 / 系统不支持),不是"节点不通"。
type icmpUnavailable struct{ err error }

func (e *icmpUnavailable) Error() string { return "ICMP 不可用: " + e.err.Error() }
func (e *icmpUnavailable) Unwrap() error { return e.err }

var pingSeq uint32

// pingHost 解析域名(只取 IPv4)后 ping 一次。
func pingHost(ctx context.Context, host string, resolve func(context.Context, string) ([]string, error), allow6 bool) (time.Duration, error) {
	host, err := lookupNode(ctx, host, resolve, allow6)
	if err != nil {
		return 0, err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		addrs, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
		if err != nil || len(addrs) == 0 {
			return 0, errors.New("解析失败")
		}
		ip = addrs[0]
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, errors.New("只支持 IPv4")
	}
	return pingICMP(ctx, ip4)
}

func pingICMP(ctx context.Context, ip net.IP) (time.Duration, error) {
	// 先试原始套接字(Windows 服务以 SYSTEM 跑、Linux 有 CAP_NET_RAW 时都能开)。
	// 开不了就退到非特权 ICMP:Android 的应用进程没有 CAP_NET_RAW,原始套接字 100% 是 EPERM,
	// 但系统的 ping_group_range 允许 SOCK_DGRAM 的 ICMP —— 不退这一步,手机 / 电视上所有
	// hysteria2 / tuic / wireguard 节点都会白白掉进"临时实例真连一次"那条慢路。
	unprivileged := false
	c, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		c, err = icmp.ListenPacket("udp4", "0.0.0.0")
		if err != nil {
			return 0, &icmpUnavailable{err}
		}
		unprivileged = true
	}
	defer c.Close()
	id := os.Getpid() & 0xffff
	seq := int(atomic.AddUint32(&pingSeq, 1) & 0xffff)
	msg := icmp.Message{Type: ipv4.ICMPTypeEcho, Code: 0, Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("godusevpn")}}
	b, err := msg.Marshal(nil)
	if err != nil {
		return 0, err
	}
	dl, ok := ctx.Deadline()
	if !ok {
		dl = time.Now().Add(probeTimeout)
	}
	_ = c.SetDeadline(dl)
	start := time.Now()
	var dst net.Addr = &net.IPAddr{IP: ip}
	if unprivileged {
		dst = &net.UDPAddr{IP: ip} // 非特权 ICMP 走的是 UDP 套接字的地址形状,端口填 0
	}
	if _, err := c.WriteTo(b, dst); err != nil {
		return 0, err
	}
	buf := make([]byte, 1500)
	for {
		n, peer, err := c.ReadFrom(buf)
		if err != nil {
			return 0, err
		}
		pkt := buf[:n]
		// Windows 的原始套接字收到的报文带 IP 头,先剥掉
		if n > 20 && pkt[0]>>4 == 4 {
			if hl := int(pkt[0]&0x0f) * 4; hl < n {
				if m, err := icmp.ParseMessage(1, pkt[hl:]); err == nil && m.Type == ipv4.ICMPTypeEchoReply {
					pkt = pkt[hl:]
				}
			}
		}
		m, err := icmp.ParseMessage(1, pkt)
		if err != nil || m.Type != ipv4.ICMPTypeEchoReply {
			continue
		}
		// 非特权 ICMP 的 ID 由内核自己分配(它拿来区分是哪个套接字的),应用写进去的那个不会原样回来,
		// 所以这一路只比对序号;对端地址也要按 IP 比 —— 那边给回来的是带 :0 的 UDP 地址。
		if e, ok := m.Body.(*icmp.Echo); ok && e.Seq == seq && (unprivileged || e.ID == id) && samePeer(peer, ip) {
			return time.Since(start), nil
		}
	}
}

// samePeer 回包是不是来自我们 ping 的那台机器。原始套接字给的是 *net.IPAddr,
// 非特权 ICMP 给的是 *net.UDPAddr(端口 0),所以按 IP 比,不能比字符串。
func samePeer(peer net.Addr, ip net.IP) bool {
	switch a := peer.(type) {
	case *net.IPAddr:
		return a.IP.Equal(ip)
	case *net.UDPAddr:
		return a.IP.Equal(ip)
	default:
		return false
	}
}
