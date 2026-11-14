package builder

import (
	"encoding/json"
	"net/netip"
	"strings"

	"github.com/Maoyangui/godusevpn/internal/profile"
)

// HintNodes 订阅里的提示节点(按订阅里的顺序)。面板拿它们在节点列表里显示用量、到期、续费,连不到任何远端服务器:
// 放进测速组是白测;放进自动选择更糟 —— 测速结果出来之前 urltest 先用排第一的成员,而提示节点常排第一,全局模式下
// 这段时间整机没网。所以不进自动选择与线路组(见 buildConfig);节点列表里照常显示,手动选它和以前一样能选上。
//
// 只认 m-ui 提示节点的形状(见 hintShape):地址只指向本机、协议不是 socks / http、端口小于 1024。m-ui 订阅里的
// 「00-勿选-流量…」是 shadowsocks 127.0.0.1:1(sub.noticeLinkURI / noticeClashProxy,从第一版起就是这样;sing-box
// JSON 订阅里不注入,经分享链接、Clash 订阅转过来的会带着),管理员自建的「续费提示」线路生产上是 shadowsocks 127.0.0.1:88。
// 写着本机地址的 socks / http 出站多半是接本机另一个代理(Tor、本机的 Clash、公司代理客户端),高端口的多半是本机转发
// (kcptun、ssh -L、测试里起的本地服务),都是真能用的节点,不算 —— 代价见 hintShape。
//
// 带 detour 的出站经 detour 那一头出去,自己的 server 不是从本机拨的:detour 是 shadowtls 时它的拨号器不看目标地址,
// server 只是个占位;是别的代理时 server 交给那一头去连(127.0.0.1 是那台服务器自己身上的服务)。写成本机地址也照样能用,
// 不能按它判。所以照 detour 一路找下去,按真正从本机拨出去的那一个判:它是提示节点的形状,这条(连同经它出去的)才是
// 提示节点。detour 指向订阅外的出站(内置的 direct 之类)时自己的 server 就是从本机拨的,照旧按它判;绕成圈的不算。
func HintNodes(p *profile.Profile) []string {
	if p == nil {
		return nil
	}
	type hop struct {
		typ, server, detour string
		port                int
	}
	hops := make(map[string]hop, len(p.Tags))
	for i, raw := range p.Outbounds {
		if i >= len(p.Tags) {
			break
		}
		var m struct {
			Type   string `json:"type"`
			Server string `json:"server"`
			Port   int    `json:"server_port"`
			Detour string `json:"detour"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		hops[p.Tags[i]] = hop{m.Type, m.Server, strings.TrimSpace(m.Detour), m.Port}
	}
	var out []string
	for _, tag := range p.Tags {
		h, ok := hops[tag]
		if !ok {
			continue
		}
		seen := map[string]bool{tag: true}
		for h.detour != "" {
			next, ok := hops[h.detour]
			if !ok {
				break // 订阅外的出站:自己的 server 从本机拨
			}
			if seen[h.detour] {
				h = hop{} // 绕成圈:配置本身起不来,不当提示节点
				break
			}
			seen[h.detour] = true
			h = next
		}
		if hintShape(h.typ, h.server, h.port) {
			out = append(out, tag)
		}
	}
	return out
}

// localProxyTypes 接本机另一个代理时用的出站类型:写着本机地址的这两种是链式代理,不是提示节点。
var localProxyTypes = map[string]bool{"socks": true, "http": true}

// hintShape 出站是不是提示节点的形状:
//   - 地址只指向本机(见 localServer);
//   - 协议不是 socks / http(那是接本机另一个代理);
//   - 地址是回环 / localhost 时,端口小于 1024(m-ui 的两种都是:1、88)。本机转发、本机代理一般都在高端口上
//     (普通用户起的程序听不了 1024 以下),写成未指定地址(0.0.0.0、::)的不看端口 —— 拿它当连接目标的只有占位。
//
// 取舍:别的面板 / 机场的提示节点写成回环高端口(比如 127.0.0.1:8888)的、或者做成 socks / http 的,不再认得 ——
// 它们照旧进自动选择和测速,和 v0.7.16 及以前一样(测不通就不会被自动选上,只是测速之前那一小段可能先落在它上面)。
// m-ui 自己的提示节点各个版本都是上面那个形状,不受影响。
func hintShape(typ, server string, port int) bool {
	if localProxyTypes[strings.ToLower(strings.TrimSpace(typ))] || !localServer(server) {
		return false
	}
	return unspecified(server) || port < 1024
}

// unspecified 地址是未指定地址(0.0.0.0、::)。
func unspecified(server string) bool {
	ip, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(server), "["), "]"))
	return err == nil && ip.Unmap().IsUnspecified()
}

// localServer 地址只指向本机:回环、未指定地址,或 localhost(RFC 6761:localhost 和它的子域名都解析到回环)。
func localServer(server string) bool {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(server)), ".")
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsUnspecified()
}
