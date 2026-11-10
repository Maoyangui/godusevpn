package builder

import (
	"encoding/json"
	"net/netip"
	"strings"

	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// V6Blocked 这一轮一定连不上的 IPv6 节点(按订阅里的顺序)。关着 IPv6 时有两种:
//
//   - server 直接写成 IPv6 地址,而连接期间各网卡的 IPv6 被停用(nicOff:桌面端开着 TUN、关着 IPv6、开着「连接时停用网卡 IPv6」,
//     见 daemon.wantNICOff)—— 拨过去就是 network unreachable,服务器一个包都收不到。
//     Android 不停用网卡:手机网络有 IPv6 就连得上,不算在这里;桌面端关掉「连接时停用网卡 IPv6」时同理。
//   - server 是域名、却只解析得到 IPv6 地址(v6Only,守护进程解析节点域名时找出来的):关着 IPv6 时内核给节点域名解析按
//     dns.strategy = ipv4_only 来(route.default_domain_resolver 只写了服务器,策略跟着它),解析不出地址,哪个平台都连不上。
//
// 不为了它们去开网卡的 IPv6、也不放宽节点域名的解析策略 ——「关着 IPv6」就是不走 IPv6,这是隐私上的承诺。
// 这些节点不进自动选择与线路组,界面标成「IPv6 节点」,选中时把原因说清楚。
func V6Blocked(p *profile.Profile, s settings.Settings, nicOff bool, v6Only map[string]bool) []string {
	if p == nil || s.IPv6 {
		return nil
	}
	var out []string
	for i, raw := range p.Outbounds {
		if i >= len(p.Tags) {
			break
		}
		var m struct {
			Server string `json:"server"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		host := strings.TrimSpace(m.Server)
		if ip, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")); err == nil {
			if nicOff && ip.Is6() && !ip.Is4In6() {
				out = append(out, p.Tags[i])
			}
			continue
		}
		if host != "" && v6Only[host] {
			out = append(out, p.Tags[i])
		}
	}
	return out
}
