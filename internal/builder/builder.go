// Package builder 把"订阅节点 + 本地设置"渲染成完整的 sing-box 配置。
//
// 策略全部在客户端决定:TUN、混合端口、DoH、fake-ip、禁 IPv6、规则 / 全局 / 直连三套路由、规则集。
// 模式切换靠路由规则里的 clash_mode 分支 + 内核 Clash API,不用重启;节点选择组的当前项由 cache_file 与设置双份记住。
package builder

import (
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

type Input struct {
	Profile     *profile.Profile
	Settings    settings.Settings
	DataDir     string // cache.db 放这里
	ClashSecret string // Clash API 密钥,服务每次启动随机生成
	RuleSetDir  string // 内置离线规则集目录:有 <tag>.srs 就用本地文件,没有走远程
	// NodeIPs 用域名写的节点服务器解析出来的地址(域名 → 地址列表)。
	// 域名节点在隧道里靠嗅探到的 SNI 命中直连规则,但不带 TLS 的协议嗅不出域名,这份是兜底。
	// 拿不到就留空,退回只按域名匹配。
	NodeIPs map[string][]string
	Darwin  bool // 按 macOS 生成:隧道网卡名由内核分配(utunN),不能写死;不传时看运行平台
	Android bool // 按 Android 生成:"进程名"是应用包名(package_name),按应用直连的应用整个绕过 VPN(exclude_package);不传时看运行平台
}

const (
	TestURL  = "http://www.gstatic.com/generate_204"
	TunName  = "godusevpn"
	tunAddr4 = "172.19.0.1/30"
	// HijackDNS macOS 上接管系统 DNS 时填的地址。走的是"进了隧道就被 hijack-dns 接住"这条路,
	// 所以填什么地址都一样(只要不是被排除在隧道外的私网段,也不能是 TUN 自己的地址 —— 那会被内核当本机地址回环)。
	// 挑一个国内外都能用的公共解析器:万一哪次崩溃没来得及还原,机器照样能解析,不至于打不开网页。
	HijackDNS     = "223.5.5.5"
	tunAddr6      = "fdfe:dcba:9876::1/126"
	fakeIP4       = "198.18.0.0/15"
	fakeIP6       = "fc00::/18"
	ruleSetBase   = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/"
	ruleSetIPBase = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/"
)

// ModeName 设置里的模式 → 内核 Clash API 里的模式名。
func ModeName(mode string) string {
	switch mode {
	case settings.ModeGlobal:
		return "Global"
	case settings.ModeDirect:
		return "Direct"
	default:
		return "Rule"
	}
}

// SettingMode 反向:内核模式名 → 设置值。
func SettingMode(clash string) string {
	switch strings.ToLower(clash) {
	case "global":
		return settings.ModeGlobal
	case "direct":
		return settings.ModeDirect
	default:
		return settings.ModeRule
	}
}

// Build 渲染配置(带缩进的 JSON,便于放进诊断包看)。
func Build(in Input) ([]byte, error) {
	android := in.Android || runtime.GOOS == "android"
	darwin := in.Darwin || runtime.GOOS == "darwin"
	procKey := "process_name" // 桌面按进程名分流;Android 没有进程名,按应用包名
	if android {
		procKey = "package_name"
	}
	if in.Profile == nil || len(in.Profile.Outbounds) == 0 {
		return nil, errors.New("没有节点")
	}
	s := in.Settings
	if err := s.Validate(); err != nil {
		return nil, err
	}
	tags := in.Profile.Tags
	selected := "auto"
	for _, t := range tags {
		if t == s.Selected {
			selected = t
		}
	}

	// ---- 出站 ----
	outbounds := []any{
		obj("type", "selector", "tag", "proxy", "outbounds", append([]string{"auto"}, tags...), "default", selected, "interrupt_exist_connections", true),
		obj("type", "urltest", "tag", "auto", "outbounds", tags, "url", TestURL, "interval", itoa(s.ProbeMinutes)+"m", "tolerance", 50), // 定时测速:每隔 ProbeMinutes 分钟测一轮
	}
	for _, raw := range in.Profile.Outbounds {
		outbounds = append(outbounds, json.RawMessage(raw))
	}
	outbounds = append(outbounds, obj("type", "direct", "tag", "direct"))

	// ---- DNS ----
	strategy := "ipv4_only"
	if s.IPv6 {
		strategy = "prefer_ipv4"
	}
	servers := []any{obj("type", "local", "tag", "system")} // 系统 DNS:只用来解析 DoH 服务器自己的域名
	remote := obj("type", "https", "tag", "remote", "server", s.RemoteDNS, "detour", "proxy")
	if !settings.IsIP(s.RemoteDNS) {
		remote["domain_resolver"] = "system"
	}
	servers = append(servers, remote)
	if s.LocalDNS == "system" {
		servers = append(servers, obj("type", "local", "tag", "local"))
	} else {
		local := obj("type", "https", "tag", "local", "server", s.LocalDNS)
		if !settings.IsIP(s.LocalDNS) {
			local["domain_resolver"] = "system"
		}
		servers = append(servers, local)
	}
	if s.FakeIP {
		fake := obj("type", "fakeip", "tag", "fakeip", "inet4_range", fakeIP4)
		if s.IPv6 {
			fake["inet6_range"] = fakeIP6
		}
		servers = append(servers, fake)
	}
	dnsRules := []any{
		obj("outbound", "any", "server", "local"), // 节点自己的域名:直连解析,不能绕圈
		obj("clash_mode", "Direct", "server", "local"),
		obj("clash_mode", "Rule", "rule_set", []string{"geosite-cn"}, "server", "local"),
	}
	if s.FakeIP {
		qt := []string{"A"}
		if s.IPv6 {
			qt = append(qt, "AAAA")
		}
		dnsRules = append(dnsRules, obj("query_type", qt, "server", "fakeip"))
	}
	dns := obj("servers", servers, "rules", dnsRules, "final", "remote", "strategy", strategy)

	// ---- 入站 ----
	var inbounds []any
	if s.TUN {
		// v6 地址一直加:关掉 IPv6 时也要把 v6 流量接进隧道,否则它绕过隧道从物理网卡直接出网(泄露),
		// 进来之后由下面的 ip_version=6 拒绝规则丢掉,应用会立刻回退到 IPv4。
		addr := []string{tunAddr4, tunAddr6}
		stack := s.TUNStack
		if darwin && stack != "gvisor" {
			// macOS 上系统协议栈这条路走不通:sing-tun 把 utun 配成指向自己的点对点口(172.19.0.1 --> 172.19.0.1),
			// 系统栈要把包绕回本机才能完成握手,绕不回去 —— 真机验收实测内核一条入站 TCP 连接都收不到、nc 直接超时,
			// 换成纯用户态的 gvisor 立刻就通。所以这里不管设置里选了什么,macOS 一律用 gvisor。
			stack = "gvisor"
		}
		tun := obj("type", "tun", "tag", "tun-in", "address", addr,
			"auto_route", true, "strict_route", s.StrictRoute, "stack", stack)
		if !darwin {
			// macOS 的隧道网卡只能叫 utunN(内核分配),写死名字内核直接拒绝、内核起不来;留空让 sing-box 自己算
			tun["interface_name"] = TunName
		}
		if s.LANBypass {
			// 私网段与链路本地之外,168.63.129.16 是 Azure 平台地址(来宾代理、DNS、健康探测),进了隧道整台云主机就失联,一并排除
			ex := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "168.63.129.16/32", "224.0.0.0/4"}
			ex = append(ex, "fc00::/7", "fe80::/10", "ff00::/8") // v6 的私网 / 链路本地 / 组播,同样留给局域网
			tun["route_exclude_address"] = ex
		}
		if s.NetMode == settings.NetGateway {
			// 网关模式(Linux 软路由):经本机转发的局域网流量由 sing-box 用 nftables 直接导入(比策略路由快),需要内核带 nftables
			tun["auto_redirect"] = true
		}
		if android && len(s.BypassApps) > 0 {
			tun["exclude_package"] = s.BypassApps // 这些应用整个不进 VPN(VpnService.addDisallowedApplication)
		}
		inbounds = append(inbounds, tun)
	}
	if s.MixedPort > 0 {
		inbounds = append(inbounds, obj("type", "mixed", "tag", "mixed-in", "listen", "127.0.0.1", "listen_port", s.MixedPort))
	}

	// ---- 路由 ----
	ruleSets := []any{
		ruleSet("geosite-cn", ruleSetBase+"geosite-cn.srs", in.RuleSetDir),
		ruleSet("geoip-cn", ruleSetIPBase+"geoip-cn.srs", in.RuleSetDir),
	}
	if s.AdBlock {
		ruleSets = append(ruleSets, ruleSet("geosite-category-ads-all", ruleSetBase+"geosite-category-ads-all.srs", in.RuleSetDir))
	}
	rules := []any{
		obj("action", "sniff"),
		obj("protocol", "dns", "action", "hijack-dns"),
		obj("port", 53, "action", "hijack-dns"), // 不走系统解析、自己发 53 的程序也收进来,不泄漏
	}
	if len(s.BypassApps) > 0 {
		rules = append(rules, obj(procKey, s.BypassApps, "outbound", "direct")) // 指定进程 / 应用直连,放在最前
	}
	// 局域网设备策略(网关模式):按来源 IP 强制直连 / 拒绝 / 代理,放在模式分支之前,任何模式下都成立
	if s.NetMode == settings.NetGateway {
		byMode := map[string][]string{}
		for _, d := range s.Devices {
			if d.Mode == "" || d.IP == "" {
				continue
			}
			byMode[d.Mode] = append(byMode[d.Mode], d.IP+cidrSuffix(d.IP))
		}
		for _, mode := range []string{"reject", "direct", "proxy"} {
			if ips := byMode[mode]; len(ips) > 0 {
				if mode == "reject" {
					rules = append(rules, obj("source_ip_cidr", ips, "action", "reject"))
				} else {
					rules = append(rules, obj("source_ip_cidr", ips, "outbound", mode))
				}
			}
		}
	}
	if !s.IPv6 {
		// 先按 ipv4_only 把目标域名解析成 IPv4(fake-ip 只是给客户端的占位,这里查的是真实地址),
		// 之后不管直连还是经代理,拿到的都是 IPv4;否则代理服务器会自己解析出 AAAA 走 IPv6 出去。
		// 解析走 DNS 规则:国内域名本地 DoH、其余远程 DoH(经代理),路由器查询不会返回 fake-ip。
		rules = append(rules,
			obj("action", "resolve", "strategy", "ipv4_only"),
			obj("ip_version", 6, "action", "reject")) // 直接写 IPv6 字面量的连接也堵住
	} else {
		rules = append(rules, obj("action", "resolve")) // 开 IPv6 也要先解析,否则 IP 类规则(geoip、IP 段)对域名连接不生效
	}
	// 节点服务器本身一律直连,而且要排在所有模式规则前面。
	// 不这么做的话,内核自己去连节点(定时测速要把每个节点都连一遍)的那一步会被自己的 TUN 接住,
	// 再按当前模式转出去 —— 全局模式下就变成"本机 → 隧道 → 当前节点 → 目标节点",白绕一跳、
	// 流量算两份,测出来的延迟也不是节点的真实延迟;要是主节点本身就是被绕的那个,还会自己套自己。
	// 真机上实测过:hysteria2(UDP)那些没事,anytls(TCP)那些每次测速都在绕圈。
	// 只放行"这台服务器上的这些端口",不是整台服务器:面板、订阅地址、落地页常和节点同一个 IP,
	// 按整个 IP 放行会把它们也变成直连,用户在全局模式下会莫名其妙地把面板暴露给本地网络。
	for _, n := range nodeAddrs(in.Profile) {
		r := obj("port", n.Ports, "outbound", "direct")
		if n.IsIP {
			r["ip_cidr"] = []string{n.Host}
			rules = append(rules, r)
			continue
		}
		r["domain"] = []string{n.Host}
		rules = append(rules, r)
		// 一条规则里的字段是"与"的关系,域名和地址盖不到一条里,只能再来一条
		if cidrs := hostCIDRs(in.NodeIPs[n.Host]); len(cidrs) > 0 {
			rules = append(rules, obj("ip_cidr", cidrs, "port", n.Ports, "outbound", "direct"))
		}
	}
	dr := s.DefaultRules
	rules = append(rules, withOutbound(obj("ip_is_private", true), dr.Private))
	rules = append(rules,
		obj("clash_mode", "Direct", "outbound", "direct"),
		obj("clash_mode", "Global", "outbound", "proxy"),
	)
	// 用户规则组:只在规则模式下走到这里(上面两条 clash_mode 已把全局 / 直连截走)
	findProcess := len(s.BypassApps) > 0
	haveSet := map[string]bool{"geosite-cn": true, "geoip-cn": true, "geosite-category-ads-all": s.AdBlock}
	for _, g := range s.RuleGroups {
		if !g.Enabled || len(g.Rules) == 0 {
			continue
		}
		r, sets, proc := groupRule(g, tags, in.RuleSetDir, haveSet, procKey)
		ruleSets = append(ruleSets, sets...)
		findProcess = findProcess || proc
		rules = append(rules, r)
	}
	if s.AdBlock {
		rules = append(rules, obj("rule_set", []string{"geosite-category-ads-all"}, "action", "reject"))
	}
	rules = append(rules, withOutbound(obj("rule_set", []string{"geosite-cn", "geoip-cn"}), dr.CN))
	route := obj("rules", rules, "rule_set", ruleSets, "final", dr.Final, "auto_detect_interface", true, "default_domain_resolver", "local")
	if findProcess {
		route["find_process"] = true
	}

	cfg := obj(
		"log", obj("level", s.LogLevel, "timestamp", true),
		"dns", dns,
		"inbounds", inbounds,
		"outbounds", outbounds,
		"route", route,
		"experimental", obj(
			"clash_api", obj("external_controller", "127.0.0.1:"+itoa(s.ClashPort), "secret", in.ClashSecret, "default_mode", ModeName(s.Mode)),
			"cache_file", obj("enabled", true, "path", filepath.Join(in.DataDir, "cache.db"), "store_fakeip", true),
		),
	)
	return json.MarshalIndent(cfg, "", "  ")
}

// groupRule 把一个规则组渲染成一条路由规则:同类型的值合成一个列表,多种类型用 logical/or 组起来(单条规则里不同字段是"且")。
// 返回规则、需要新增的规则集、是否用到了进程名。
func groupRule(g settings.RuleGroup, tags []string, dir string, haveSet map[string]bool, procKey string) (map[string]any, []any, bool) {
	lists := map[string][]string{}
	var ports []int
	var portRanges, sets []string
	var newSets []any
	for _, r := range g.Rules {
		switch r.Type {
		case settings.RulePort:
			if a, b, ok := strings.Cut(r.Value, "-"); ok {
				portRanges = append(portRanges, a+":"+b)
			} else {
				n, _ := strconv.Atoi(r.Value)
				ports = append(ports, n)
			}
		case settings.RuleGeosite, settings.RuleGeoIP:
			tag, url := "geosite-"+r.Value, ruleSetBase+"geosite-"+r.Value+".srs"
			if r.Type == settings.RuleGeoIP {
				tag, url = "geoip-"+r.Value, ruleSetIPBase+"geoip-"+r.Value+".srs"
			}
			if !haveSet[tag] {
				haveSet[tag] = true
				newSets = append(newSets, ruleSet(tag, url, dir))
			}
			sets = append(sets, tag)
		default:
			lists[r.Type] = append(lists[r.Type], r.Value)
		}
	}
	var parts []any
	for _, typ := range []string{settings.RuleDomain, settings.RuleDomainSuffix, settings.RuleDomainKeyword, settings.RuleDomainRegex, settings.RuleIPCIDR, settings.RuleProcess} {
		if v := lists[typ]; len(v) > 0 {
			key := typ
			if typ == settings.RuleProcess {
				key = procKey // Android 上"进程名"条件填的是应用包名
			}
			parts = append(parts, obj(key, v))
		}
	}
	if len(ports) > 0 {
		parts = append(parts, obj("port", ports))
	}
	if len(portRanges) > 0 {
		parts = append(parts, obj("port_range", portRanges))
	}
	if len(sets) > 0 {
		parts = append(parts, obj("rule_set", sets))
	}
	var rule map[string]any
	if len(parts) == 1 {
		rule = parts[0].(map[string]any)
	} else {
		rule = obj("type", "logical", "mode", "or", "rules", parts)
	}
	switch out := g.Outbound; out {
	case settings.OutReject:
		rule["action"] = "reject"
	case settings.OutDirect, settings.OutProxy, "auto":
		rule["outbound"] = out
	default:
		rule["outbound"] = settings.OutProxy // 节点不在当前订阅里就走当前选择
		for _, t := range tags {
			if t == out {
				rule["outbound"] = out
			}
		}
	}
	return rule, newSets, len(lists[settings.RuleProcess]) > 0
}

// ruleSet 本地有离线副本就用本地(安装包内置),否则经代理从官方仓库拉,一周更新一次。
func ruleSet(tag, url, dir string) map[string]any {
	if dir != "" {
		local := filepath.Join(dir, tag+".srs")
		if st, err := os.Stat(local); err == nil && !st.IsDir() {
			return obj("tag", tag, "type", "local", "format", "binary", "path", local)
		}
	}
	return obj("tag", tag, "type", "remote", "format", "binary", "url", url, "download_detour", "proxy", "update_interval", "7d")
}

func obj(kv ...any) map[string]any {
	m := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// cidrSuffix 单个地址补成 /32 或 /128。
func cidrSuffix(ip string) string {
	if strings.Contains(ip, ":") {
		return "/128"
	}
	return "/32"
}

// hostCIDRs 把解析出来的地址转成规则要的写法,顺手挡掉不该进来的:
// 隧道开着时解析域名会拿到 fake-ip(198.18/15),把它写进直连规则等于把一整段假地址放直连。
func hostCIDRs(ips []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range ips {
		ip, err := netip.ParseAddr(strings.TrimSpace(s))
		if err != nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() {
			continue
		}
		if ip.Is4() && fakeIP4Prefix.Contains(ip) {
			continue
		}
		c := ip.String() + "/" + itoa(ip.BitLen())
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

var fakeIP4Prefix = netip.MustParsePrefix(fakeIP4)

// nodeServer 一台节点服务器,以及订阅里用到它的所有端口。
type nodeServer struct {
	Host  string // IP 写成带前缀长度的形式(1.2.3.4/32),域名原样
	IsIP  bool
	Ports []int
}

// nodeAddrs 订阅里所有节点的服务器地址,按"一台服务器一条"归并(去重,顺序稳定)。
// 域名那份靠嗅探到的 SNI 命中,IP 那份靠目的地址命中,两种写法都要能盖住。
func nodeAddrs(p *profile.Profile) []nodeServer {
	if p == nil {
		return nil
	}
	var out []nodeServer
	at := map[string]int{} // 主机 → out 里的下标
	seenPort := map[string]bool{}
	for _, raw := range p.Outbounds {
		var m struct {
			Server string `json:"server"`
			Port   int    `json:"server_port"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		h := strings.TrimSpace(m.Server)
		if h == "" || m.Port <= 0 || m.Port > 65535 {
			continue
		}
		key := h
		if i, ok := at[key]; ok {
			if !seenPort[key+":"+itoa(m.Port)] {
				seenPort[key+":"+itoa(m.Port)] = true
				out[i].Ports = append(out[i].Ports, m.Port)
			}
			continue
		}
		n := nodeServer{Host: h, Ports: []int{m.Port}}
		if ip, err := netip.ParseAddr(h); err == nil {
			n.IsIP, n.Host = true, ip.String()+"/"+itoa(ip.BitLen())
		}
		at[key] = len(out)
		seenPort[key+":"+itoa(m.Port)] = true
		out = append(out, n)
	}
	return out
}

// withOutbound 给一条规则填出口:reject 是动作,其余是出站标签。默认规则的三项可配置,统一走这里。
func withOutbound(rule map[string]any, out string) map[string]any {
	if out == settings.OutReject {
		rule["action"] = "reject"
	} else {
		rule["outbound"] = out
	}
	return rule
}
