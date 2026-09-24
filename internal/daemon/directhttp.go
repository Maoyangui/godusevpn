package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

// directHTTP 内核没跑、或者只开混合端口时,服务自己直连拉订阅 / 规则集用的客户端。
//
// 全局禁直连(全局模式 + 开关)下,域名不能交给系统解析器:Windows 上 Go 的解析是交给系统的 DNS Client 服务去发
// 的,发包的是 svchost 而不是本服务。闸只放行本服务,这组查询会被"拦 DNS"挡下 —— 而挡下之前,它是明文发给路由器
// 的,本身就是隧道外的 DNS。所以这时改用 bootResolver 的 DoH:按地址连「本地 DNS」那台,从本服务进程发出(闸放行),
// 解出地址后按地址拨号;TLS 仍按 URL 里的主机名校验。
//
// 「本地 DNS」是 system 或填的域名时没有可用的加密解析(bootResolver 返回 nil),只能照旧用系统解析 —— 禁直连下
// 多半解析不了,这是那两种设置本来的代价(节点域名在禁直连下也同样不解析)。
// 不是禁直连时行为不变。
func (d *Daemon) directHTTP() *http.Client {
	s := d.getSettings()
	if !(s.NoDirect && s.Mode == settings.ModeGlobal) {
		return d.http
	}
	resolve := d.bootResolver(s)
	if resolve == nil {
		return d.http
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	tr := &http.Transport{
		Proxy: nil, // 不走环境变量里的代理
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if net.ParseIP(host) != nil {
				return dialer.DialContext(ctx, network, addr)
			}
			ips, err := resolve(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("经 DoH 解析 %s: %w", host, err)
			}
			last := fmt.Errorf("经 DoH 解析 %s:没有 IPv4 地址", host)
			for _, ip := range ips {
				c, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
				if err == nil {
					return c, nil
				}
				last = err
			}
			return nil, last
		},
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     30 * time.Second,
	}
	return &http.Client{Timeout: d.http.Timeout, Transport: tr}
}
