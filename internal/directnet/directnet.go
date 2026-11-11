// Package directnet 本机不经代理、直接走系统网络栈发出去的请求(没连接时拉订阅、手动检查更新、下载安装包、补规则集、DoH 解析……)
// 统一的拨号规则:「允许 IPv6」关着时只走 IPv4 —— 拨号用 tcp4 / udp4(域名只解析 A 记录,写成 IPv6 的地址直接拨不出去,一个包都不发)。
//
// 隐私承诺是「关着 IPv6 就不走 IPv6,不分平台」:经内核出站的流量由配置里的 ipv4_only 管,这里管的是内核以外、程序自己发的那些。
package directnet

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// Network 关着 IPv6 时把 tcp / udp 收成 tcp4 / udp4;已经限定了协议族的、别的网络(unix 等)原样返回。
// 开着 IPv6 时什么都不改。
func Network(network string, allow6 bool) string {
	if allow6 {
		return network
	}
	switch network {
	case "tcp", "tcp6":
		return "tcp4"
	case "udp", "udp6":
		return "udp4"
	case "ip", "ip6":
		return "ip4"
	}
	return network
}

// DialFunc 拨号函数的形状(net.Dialer.DialContext、http.Transport.DialContext 用的那种)。
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Dial 包一层拨号:每次拨号前问一次 allow6(设置随时可能改),关着就只拨 IPv4。allow6 为 nil 按关着算。
func Dial(dial DialFunc, allow6 func() bool) DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dial(ctx, Network(network, allow6 != nil && allow6()), addr)
	}
}

// Transport 系统网络栈直连用的 Transport:参数同 http.DefaultTransport(含环境变量里的代理 —— 代理地址同样只拨 IPv4),
// 拨号经 Dial 收口。control 是套接字的额外设置(Linux 上守护进程给自己的直连打放行标记),可以为 nil。
//
// 不留空闲连接:开关只在拨号时判,池子里留着的连接再用时不会再判 —— 开着 IPv6 时建的那条,关掉以后还会被拿来用。
// 这些请求都是偶尔一次(拉订阅、检查更新、下载安装包),不复用连接不碍事。
func Transport(allow6 func() bool, control func(network, address string, c syscall.RawConn) error) *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	if control != nil {
		dialer.Control = control
	}
	tr.DialContext = Dial(dialer.DialContext, allow6)
	tr.DisableKeepAlives = true
	return tr
}

// Client 带 Transport 的客户端。
func Client(timeout time.Duration, allow6 func() bool) *http.Client {
	return &http.Client{Timeout: timeout, Transport: Transport(allow6, nil)}
}

// KeepV4 关着 IPv6 时从解析结果里只留 IPv4 地址(IPv4 映射的 IPv6 地址算 IPv4);开着时原样返回。
func KeepV4(addrs []string, allow6 bool) []string {
	if allow6 {
		return addrs
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		ip, err := netip.ParseAddr(strings.TrimSpace(a))
		if err != nil {
			continue
		}
		if ip = ip.Unmap(); ip.Is4() {
			out = append(out, ip.String())
		}
	}
	return out
}
