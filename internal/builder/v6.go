package builder

import (
	"encoding/json"
	"net/netip"
	"strings"

	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// V6BlockTag 关着 IPv6 时兜住"地址写成 IPv6 的节点"的出站(type block):这些节点的出站改经它拨号,一拨就被拒,
// 本机一个 IPv6 包都不发(见 v6Detours)。这个名字在订阅里是保留名(profile.reservedTags),节点、线路组都撞不上。
const V6BlockTag = "block"

// resolveOnDetour 带 detour 也在本机解析服务器域名的出站类型(sing-box 里 ResolverOnDetour 的那几种):
// 它们的域名只有 IPv6 地址时,本机按 ipv4_only 解析不出来,照样连不上。
var resolveOnDetour = map[string]bool{"naive": true}

// v6Node 判 IPv6 节点用到的那几个字段。
type v6Node struct {
	Type   string `json:"type"`
	Server string `json:"server"`
	Detour string `json:"detour"`
}

// ownV6 这个节点自己的服务器地址要从本机走 IPv6 才连得上:写成 IPv6 地址(literal),或者是只解析得到 IPv6 地址的域名。
func ownV6(server string, v6Only map[string]bool) (literal, v6 bool) {
	host := strings.TrimSpace(server)
	if ip, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")); err == nil {
		return ip.Is6() && !ip.Is4In6(), ip.Is6() && !ip.Is4In6()
	}
	return false, host != "" && v6Only[host]
}

// V6Blocked 这一轮不用的 IPv6 节点(按订阅里的顺序)。「允许 IPv6」关着时,本机连这个节点要走 IPv6 的,一概不用 ——
// 关着 IPv6 就是不走 IPv6,这是隐私上的承诺,不分平台:
//
//   - server 直接写成 IPv6 地址:本机拨过去就是 IPv6。桌面端停用了网卡 IPv6 时拨不通;Android、桌面不开 TUN、
//     关掉「连接时停用网卡 IPv6」时网络有 IPv6 就拨得通 —— 可那就是在走 IPv6,同样不用。配置里这种节点改经 block 出站
//     (V6BlockTag),手动选它内核也不去拨,一个包都不发。
//   - server 是域名、却只解析得到 IPv6 地址(v6Only,守护进程解析节点域名时找出来的):关着 IPv6 时内核给节点域名解析按
//     dns.strategy = ipv4_only 来(route.default_domain_resolver 只写了服务器,策略跟着它),解析不出地址,哪个平台都连不上。
//
// 带 detour 的链式节点:本机只连链的第一跳,后面那一跳由上一跳的服务器去连、去解析(sing-box 的 DetourDialer),
// 所以按第一跳判 —— 中转是 IPv4 的,落地写成 IPv6 也照样能用;第一跳要走 IPv6 的,整条链都不用。
// 例外是 naive 这类带 detour 也在本机解析域名的(resolveOnDetour),自己的域名只有 IPv6 地址时同样不用。
// detour 是 direct(本机直连)按它自己算;指向订阅里没有的出站、链成环的判不了,不算(内核本来也建不起来)。
//
// 不为了它们去开网卡的 IPv6、也不放宽节点域名的解析策略。这些节点不进自动选择与线路组,界面标成「IPv6 节点」,
// 选中时把原因说清楚(去设置 → 隐私打开「允许 IPv6」)。
func V6Blocked(p *profile.Profile, s settings.Settings, v6Only map[string]bool) []string {
	if p == nil || s.IPv6 {
		return nil
	}
	nodes := make(map[string]v6Node, len(p.Tags))
	for i, raw := range p.Outbounds {
		if i >= len(p.Tags) {
			break
		}
		var n v6Node
		if json.Unmarshal(raw, &n) != nil {
			continue
		}
		nodes[p.Tags[i]] = n
	}
	// blocked 按第一跳判;on 是这条链上已经走过的(成环就判不了)
	var blocked func(tag string, on map[string]bool) bool
	blocked = func(tag string, on map[string]bool) bool {
		n, ok := nodes[tag]
		if !ok || on[tag] {
			return false
		}
		_, v6 := ownV6(n.Server, v6Only)
		if n.Detour == "" || n.Detour == "direct" {
			return v6
		}
		if v6 && resolveOnDetour[n.Type] && !isIPLiteral(n.Server) {
			return true // 带 detour 也在本机解析:自己的域名解析不出 IPv4 就连不上
		}
		on[tag] = true
		return blocked(n.Detour, on)
	}
	var out []string
	for i := range p.Outbounds {
		if i >= len(p.Tags) {
			break
		}
		if blocked(p.Tags[i], map[string]bool{}) {
			out = append(out, p.Tags[i])
		}
	}
	return out
}

func isIPLiteral(server string) bool {
	_, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(server), "["), "]"))
	return err == nil
}

// v6Detours 关着 IPv6 时,地址直接写成 IPv6、由本机自己拨的节点(没有 detour,或 detour 是 direct)要改经 block 出站:
// 返回这些节点的 tag。域名只有 IPv6 地址的不用改(本机解析不出地址,本来就拨不出去),链式节点经第一跳(第一跳被挡,整条链就被挡)。
func v6Detours(p *profile.Profile, blocked []string) map[string]bool {
	if len(blocked) == 0 {
		return nil
	}
	is := make(map[string]bool, len(blocked))
	for _, t := range blocked {
		is[t] = true
	}
	var out map[string]bool
	for i, raw := range p.Outbounds {
		if i >= len(p.Tags) || !is[p.Tags[i]] {
			continue
		}
		var n v6Node
		if json.Unmarshal(raw, &n) != nil {
			continue
		}
		if literal, _ := ownV6(n.Server, nil); literal && (n.Detour == "" || n.Detour == "direct") {
			if out == nil {
				out = map[string]bool{}
			}
			out[p.Tags[i]] = true
		}
	}
	return out
}

// viaBlock 把节点出站改成经 block 出站拨号(只改 detour 一项,别的原样保留)。
func viaBlock(raw json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	m["detour"] = json.RawMessage(`"` + V6BlockTag + `"`)
	b, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return b
}
