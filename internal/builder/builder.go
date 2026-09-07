// Package builder 把"订阅节点 + 本地设置"渲染成完整的 sing-box 配置。
//
// 策略全部在客户端决定:TUN、混合端口、DoH、fake-ip、禁 IPv6、规则 / 全局 / 直连三套路由、规则集。
// 模式切换靠路由规则里的 clash_mode 分支 + 内核 Clash API,不用重启;节点选择组的当前项由 cache_file 与设置双份记住。
package builder

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
}

const (
	TestURL       = "http://www.gstatic.com/generate_204"
	TunName       = "godusevpn"
	tunAddr4      = "172.19.0.1/30"
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
		obj("type", "urltest", "tag", "auto", "outbounds", tags, "url", TestURL, "interval", "3m", "tolerance", 50),
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
		addr := []string{tunAddr4}
		if s.IPv6 {
			addr = append(addr, tunAddr6)
		}
		tun := obj("type", "tun", "tag", "tun-in", "interface_name", TunName, "address", addr,
			"auto_route", true, "strict_route", s.StrictRoute, "stack", s.TUNStack)
		if s.LANBypass {
			tun["route_exclude_address"] = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "224.0.0.0/4"}
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
	if !s.IPv6 {
		// 先按 ipv4_only 把目标域名解析成 IPv4(fake-ip 只是给客户端的占位,这里查的是真实地址),
		// 之后不管直连还是经代理,拿到的都是 IPv4;否则代理服务器会自己解析出 AAAA 走 IPv6 出去。
		// 解析走 DNS 规则:国内域名本地 DoH、其余远程 DoH(经代理),路由器查询不会返回 fake-ip。
		rules = append(rules,
			obj("action", "resolve", "strategy", "ipv4_only"),
			obj("ip_version", 6, "action", "reject")) // 直接写 IPv6 字面量的连接也堵住
	}
	rules = append(rules,
		obj("ip_is_private", true, "outbound", "direct"),
		obj("clash_mode", "Direct", "outbound", "direct"),
		obj("clash_mode", "Global", "outbound", "proxy"),
	)
	if s.AdBlock {
		rules = append(rules, obj("rule_set", []string{"geosite-category-ads-all"}, "action", "reject"))
	}
	rules = append(rules, obj("rule_set", []string{"geosite-cn", "geoip-cn"}, "outbound", "direct"))
	route := obj("rules", rules, "rule_set", ruleSets, "final", "proxy", "auto_detect_interface", true, "default_domain_resolver", "local")

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
