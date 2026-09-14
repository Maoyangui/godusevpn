package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"

	"github.com/Maoyangui/godusevpn/internal/core"
	"github.com/Maoyangui/godusevpn/internal/profile"
)

// 未连接时的测速:不经任何出站,直接量本机到节点服务器的往返。
// TCP 类协议(vmess / vless / trojan / shadowsocks / anytls …)量 TCP 握手时间;
// UDP 类协议(hysteria2 / tuic / wireguard …)服务器不听 TCP,量 ICMP ping(服务以 SYSTEM 跑,能开原始套接字);
// ping 不通(服务器或用户所在网络拦了 ICMP、本机开不了原始套接字)的节点退回"临时实例经出站真连一次",
// 不直接判"不通"。已连接时另有一套:经内核出站做 URL 测试。

const probeTimeout = 4 * time.Second

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
}

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
func probeDirect(ctx context.Context, p *profile.Profile, onEach func(tag string, ms int)) map[string]int {
	res := map[string]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	var fallback []string // ping 没通的 UDP 节点,稍后用临时实例真连一次
	for _, e := range endpoints(p) {
		if e.Server == "" || e.Port <= 0 {
			mu.Lock()
			res[e.Tag] = -1
			mu.Unlock()
			if onEach != nil {
				onEach(e.Tag, -1)
			}
			continue
		}
		wg.Add(1)
		go func(e endpoint) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			tctx, cancel := context.WithTimeout(ctx, probeTimeout)
			defer cancel()
			var d time.Duration
			var err error
			if udpProtocols[e.Type] {
				d, err = pingHost(tctx, e.Server)
				if err != nil {
					// ping 不通不等于节点不通:服务器防火墙丢 ICMP、用户所在网络(校园网、公司网)不放行 ICMP
					// 都很常见,本机开不了原始套接字也走这里。这些节点改用临时实例真连一次再下结论。
					mu.Lock()
					fallback = append(fallback, e.Tag)
					mu.Unlock()
					return
				}
			} else {
				d, err = tcping(tctx, net.JoinHostPort(e.Server, strconv.Itoa(e.Port)))
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
	if len(fallback) > 0 {
		for k, v := range core.Probe(ctx, p.Outbounds, fallback, "", onEach) {
			res[k] = v
		}
	}
	return res
}

func tcping(ctx context.Context, addr string) (time.Duration, error) {
	start := time.Now()
	c, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
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
func pingHost(ctx context.Context, host string) (time.Duration, error) {
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
	c, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return 0, &icmpUnavailable{err}
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
	if _, err := c.WriteTo(b, &net.IPAddr{IP: ip}); err != nil {
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
		if e, ok := m.Body.(*icmp.Echo); ok && e.ID == id && e.Seq == seq && peer != nil && peer.String() == ip.String() {
			return time.Since(start), nil
		}
	}
}
