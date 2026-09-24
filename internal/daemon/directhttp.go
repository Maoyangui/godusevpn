package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/Maoyangui/godusevpn/internal/builder"
)

// sealedDoH 闸开着、「本地 DNS」又没有可用的加密解析(system 或填的域名)时用的 DoH 替身,和生成配置时一致。
// 变量是为了测试能换成连不上的地址。
var sealedDoH = builder.SealedBootstrapDoH

// directHTTP 内核没跑时,服务自己直连拉订阅用的客户端("订阅刷新的回退"是禁直连下明写的例外)。
//
// 闸开着时,域名不能交给系统解析器:Windows 上 Go 的解析交给系统的 DNS Client 服务去发,发包的是 svchost 而不是
// 本服务,闸只放行本服务,这组查询会被"拦 DNS"挡下;挡下之前,它是明文发给路由器的,本身就是隧道外的 DNS。
// Linux / macOS 上守护进程是 root,闸放行它,明文查询会直接出去。所以闸开着时改用 DoH:按地址连「本地 DNS」那台
// (没有可用的就用 sealedDoH),从本服务进程发出,解出地址后按地址拨号;TLS 仍按 URL 里的主机名校验。
// 闸没开时行为不变。
func (d *Daemon) directHTTP() *http.Client {
	if !d.guardArmed() {
		return d.http
	}
	resolve := d.bootResolver(d.getSettings())
	if resolve == nil {
		server := sealedDoH
		resolve = func(ctx context.Context, host string) ([]string, error) { return lookupDoH(ctx, server, host) }
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
		DisableKeepAlives:   true, // 拉一次就完:不在隧道外留一条空闲连接
	}
	return &http.Client{Timeout: d.http.Timeout, Transport: tr}
}

// errSealed 闸开着、隧道没起来时,服务自己不从本机直连出去。
var errSealed = errors.New("全局禁直连的闸开着、隧道还没起来:这时不从本机直连出去,稍后再试")

// SelfHTTP 服务自己访问外网用的客户端(更新检查、下载安装包、补规则集;拉订阅另走 fetchProfile)。
//
// 闸开着时服务进程是被放行的(Windows 按本服务放行,Linux / macOS 按 root 放行),它直连出去就是隧道外的流量,
// 严格全局下不允许。所以:闸开着且内核在跑 → 经代理出站;闸开着而内核没跑 → 不出去(errSealed);闸没开 → 照常。
// 只开混合端口(TUN 关)时服务自己的连接不进任何隧道,这一条尤其要紧。
func (d *Daemon) SelfHTTP(timeout time.Duration) (*http.Client, error) {
	if !d.guardArmed() {
		return &http.Client{Timeout: timeout}, nil
	}
	if !d.core.Running() {
		return nil, errSealed
	}
	return d.core.HTTPClient("proxy", timeout)
}
