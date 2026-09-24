package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
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

// errArmedMidway 闸没开时开始的直连请求,途中闸开了:剩下的不再在隧道外进行。
var errArmedMidway = errors.New("全局禁直连的闸开了:在隧道外进行的直连请求停下,稍后再试")

// SelfHTTP 服务自己访问外网用的客户端(更新检查、下载安装包、补规则集;拉订阅另走 fetchProfile)。
//
// 闸开着时服务进程是被放行的(Windows 按本服务放行,Linux / macOS 按 root 放行),它直连出去就是隧道外的流量,
// 严格全局下不允许。所以:闸开着且内核在跑 → 经代理出站;闸开着而内核没跑 → 不出去(errSealed);闸没开 → 照常。
// 只开混合端口(TUN 关)时服务自己的连接不进任何隧道,这一条尤其要紧。
//
// 闸没开时给的客户端不是一次定终身:每个请求发出前再判一次闸,直连的响应体每次读之前也判 —— 否则闸没开时开始的
// 下载(更新安装包动辄几十 MB、之后还要拿 SHA256SUMS 和签名),用户途中点了连接、闸开了,剩下的照旧在隧道外。
func (d *Daemon) SelfHTTP(timeout time.Duration) (*http.Client, error) {
	if !d.guardArmed() {
		return &http.Client{Timeout: timeout, Transport: unarmedTransport{d}}, nil
	}
	if !d.core.Running() {
		return nil, errSealed
	}
	return d.core.HTTPClient("proxy", timeout)
}

// unarmedTransport 只在闸没开时直连;闸一开,新请求拒绝、在读的响应体停下。
type unarmedTransport struct{ d *Daemon }

func (t unarmedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.d.guardArmed() {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, errArmedMidway
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &armedStopBody{ReadCloser: resp.Body, d: t.d}
	return resp, nil
}

// armedStopBody 读之前判闸:闸开了就不再读(连接随 Close 关掉)。
type armedStopBody struct {
	io.ReadCloser
	d *Daemon
}

func (b *armedStopBody) Read(p []byte) (int, error) {
	if b.d.guardArmed() {
		return 0, errArmedMidway
	}
	return b.ReadCloser.Read(p)
}

// cancelOnGuard 闸一开就调 cancel(每 100 毫秒看一次),直到 ctx 结束或调用返回的 stop。
// 给只在闸没开时才做、又可能跑很久的直连动作用(比如未连接时的测速):用户途中点了连接,剩下的不许再在隧道外做。
func (d *Daemon) cancelOnGuard(ctx context.Context, cancel context.CancelFunc) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if d.guardArmed() {
					cancel()
					return
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}
