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
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 隧道起来之前的域名解析(节点服务器用域名写的时候要先把它解成地址)。
//
// 以前直接用系统解析器:那是明文 53 发到路由器 / 运营商的 DNS,节点的域名就这么露出去了。
// 全局禁直连的本意是"隧道以外一个字节都不放",这一步是唯一绕不开的直连 —— 节点自己的地址总得先解出来 ——
// 那就至少用加密的 DoH,和内核给节点域名配的那台(设置里的「本地 DNS」)保持同一台。
// 「本地 DNS」设成 system、或填的是域名 DoH(解析它自己又得先走系统解析器)时,闸开着就干脆不解析
// (节点按域名匹配直连规则,靠嗅探到的 SNI 也能命中),闸没开的模式下才退回系统解析器。

// bootResolver 返回一个按当前设置解析节点域名的函数;返回 nil 表示这一轮不解析。
func (d *Daemon) bootResolver(s settings.Settings) func(ctx context.Context, host string) ([]string, error) {
	sealed := s.NoDirect && s.Mode == settings.ModeGlobal
	if s.LocalDNS == "system" {
		if sealed {
			return nil // 明文系统解析在禁直连下一个包都不该发
		}
		return func(ctx context.Context, host string) ([]string, error) {
			return net.DefaultResolver.LookupHost(ctx, host)
		}
	}
	if !settings.IsIP(s.LocalDNS) {
		// DoH 服务器自己是域名:解它又得先走系统解析器,禁直连下同样不做
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

// lookupDoH RFC 8484:POST application/dns-message 到 https://<ip>/dns-query,只查 A(IPv6 全程禁用)。
// 服务器按地址连,证书按地址校验(223.5.5.5、1.1.1.1 这些公共 DoH 的证书都带地址),不经任何代理。
func lookupDoH(ctx context.Context, server, host string) ([]string, error) {
	name, err := dnsmessage.NewName(host + ".")
	if err != nil {
		return nil, err
	}
	msg := dnsmessage.Message{
		Header:    dnsmessage.Header{RecursionDesired: true},
		Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
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
		if r, ok := a.Body.(*dnsmessage.AResource); ok {
			addrs = append(addrs, net.IP(r.A[:]).String())
		}
	}
	if len(addrs) == 0 {
		return nil, errors.New("没有 A 记录")
	}
	return addrs, nil
}
