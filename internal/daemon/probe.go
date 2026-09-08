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
// ICMP 开不了或被拦时,这几个节点退回"临时实例经出站测一次"。已连接时另有一套:经内核出站做 URL 测试。

const probeTimeout = 4 * time.Second

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
func probeDirect(ctx context.Context, p *profile.Profile) map[string]int {
	res := map[string]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	var icmpBroken atomic.Bool
	var fallback []string
	for _, e := range endpoints(p) {
		if e.Server == "" || e.Port <= 0 {
			mu.Lock()
			res[e.Tag] = -1
			mu.Unlock()
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
				var perr *icmpUnavailable
				if errors.As(err, &perr) {
					icmpBroken.Store(true)
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
			mu.Lock()
			if err != nil {
				res[e.Tag] = -1
			} else {
				res[e.Tag] = ms
			}
			mu.Unlock()
		}(e)
	}
	wg.Wait()
	if icmpBroken.Load() && len(fallback) > 0 {
		for k, v := range core.Probe(ctx, p.Outbounds, fallback, "") {
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
