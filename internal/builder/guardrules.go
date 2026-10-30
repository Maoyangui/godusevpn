package builder

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/sagernet/sing-box/common/srs"
	C "github.com/sagernet/sing-box/constant"
	"go4.org/netipx"

	"github.com/Maoyangui/godusevpn/internal/ruleset"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// GuardRules 规则模式下「防流量逃逸」交给防火墙的东西:把路由规则折算成防火墙认得的形式。
//
// 隧道好好的时候防火墙用不上这些:应用的流量都进隧道,由内核按规则分流,内核的直连出站是本服务发的,本来就放行。
// 它们管的是内核不在的时候(崩了、在重启、设备刚开机、服务被杀):这时规则由防火墙照着执行 ——
// 规则说直连的照常直连,规则说走隧道的只能等。
//
// 防火墙只认 IP 和端口,所以按域名、进程写的规则这时管不到:
//   - 走直连的:拦(等隧道恢复)。宁可晚点通,不放行认不准的流量。
//   - 走隧道的:目的地不在放行段里就拦;万一解析到了放行段里的地址(比如想从海外看的国内站),防火墙分不出来。
type GuardRules struct {
	Direct       []netip.Prefix // 隧道异常时照常放行的地址段(已合并、互不重叠)
	TunnelPorts  []string       // 规则明确走隧道(或拒绝)的端口,"443" 或 "8000-9000":压在放行段上面,去哪都不放
	DeviceBlock  []string       // 网关模式:设成走代理 / 拒绝的设备(MAC 或 IP),转发时不看放行段
	DeviceDirect []string       // 网关模式:设成直连的设备(MAC 或 IP)
}

// 私网段:和 lanDest 判"全是局域网地址"用的是同一份(publicCIDRs 减掉的那些),默认规则「私网」按它算。
var privatePrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range privateCIDRs {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// RuleGuard 把当前设置里的规则折算成防火墙放行的东西。规则集读不到就当那一条没有(少放行,不多放行)。
//
// 路由规则是排前面的先赢,这里从后往前叠:先按「其余」定底色,再依次盖上国内、私网、用户规则组(最后盖的是排最前的组),
// 结果就是每个地址按第一条命中的规则走。和 Build 里 rules 的顺序对应,改那边的顺序要一起改。
func RuleGuard(s settings.Settings, ruleSetDir string) GuardRules {
	var b netipx.IPSetBuilder
	apply := func(outbound string, prefixes []netip.Prefix) {
		for _, p := range prefixes {
			if outbound == settings.OutDirect {
				b.AddPrefix(p)
			} else {
				b.RemovePrefix(p)
			}
		}
	}
	dr := s.DefaultRules
	if dr.Final == settings.OutDirect {
		apply(settings.OutDirect, []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")})
	}
	apply(dr.CN, geoIPPrefixes(ruleSetDir, "geoip-cn"))
	apply(dr.Private, privatePrefixes)
	var r GuardRules
	for i := len(s.RuleGroups) - 1; i >= 0; i-- {
		g := s.RuleGroups[i]
		if !g.Enabled {
			continue
		}
		for _, rule := range g.Rules {
			switch rule.Type {
			case settings.RuleIPCIDR:
				if p, err := parsePrefix(rule.Value); err == nil {
					apply(g.Outbound, []netip.Prefix{p})
				}
			case settings.RuleGeoIP:
				apply(g.Outbound, geoIPPrefixes(ruleSetDir, "geoip-"+rule.Value))
			case settings.RulePort:
				if g.Outbound != settings.OutDirect {
					r.TunnelPorts = append(r.TunnelPorts, rule.Value)
				}
			}
		}
	}
	// 不管规则怎么写都不放行的:假地址没有真实目的地;关了 IPv6 时 v6 一律不出去(内核那边是 ip_version=6 拒绝)
	b.RemovePrefix(netip.MustParsePrefix(fakeIP4))
	b.RemovePrefix(netip.MustParsePrefix(fakeIP6))
	if !s.IPv6 {
		b.RemovePrefix(netip.MustParsePrefix("::/0"))
	}
	if set, err := b.IPSet(); err == nil {
		r.Direct = set.Prefixes()
	}
	if s.NetMode == settings.NetGateway {
		for _, d := range s.Devices {
			who := d.MAC
			if who == "" {
				who = d.IP
			}
			switch {
			case who == "" || d.Mode == "":
			case d.Mode == "direct":
				r.DeviceDirect = append(r.DeviceDirect, who)
			default:
				r.DeviceBlock = append(r.DeviceBlock, who)
			}
		}
	}
	return r
}

// RuleGuardKey RuleGuard 的输入指纹。折算要解析规则集(几千个地址段),守护进程每轮同步都要用,指纹没变就用上次的结果。
func RuleGuardKey(s settings.Settings, ruleSetDir string) string {
	in, _ := json.Marshal(struct {
		Groups  []settings.RuleGroup
		Default settings.DefaultRules
		IPv6    bool
		Net     string
		Devices []settings.Device
	}{s.RuleGroups, s.DefaultRules, s.IPv6, s.NetMode, s.Devices})
	var b strings.Builder
	b.Write(in)
	// 规则集可能后来才补下来、或者被用户换掉
	tags := []string{"geoip-cn"}
	for _, g := range s.RuleGroups {
		for _, r := range g.Rules {
			if r.Type == settings.RuleGeoIP {
				tags = append(tags, "geoip-"+r.Value)
			}
		}
	}
	for _, tag := range tags {
		for _, dir := range ruleset.Dirs(ruleSetDir) {
			if st, err := os.Stat(filepath.Join(dir, tag+".srs")); err == nil {
				fmt.Fprintf(&b, "|%s:%d:%d", tag, st.Size(), st.ModTime().UnixNano())
				break
			}
		}
	}
	return b.String()
}

func parsePrefix(v string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(v); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(v)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// geoIPPrefixes 读一个 geoip 规则集里的地址段。
func geoIPPrefixes(root, tag string) []netip.Prefix {
	path, ok := ruleset.Find(root, tag)
	if !ok {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	rs, err := srs.Read(f, true)
	if err != nil {
		return nil
	}
	var out []netip.Prefix
	for _, rule := range rs.Options.Rules {
		if rule.Type != "" && rule.Type != C.RuleTypeDefault {
			continue
		}
		if rule.DefaultOptions.Invert {
			continue
		}
		for _, v := range rule.DefaultOptions.IPCIDR {
			if p, err := parsePrefix(v); err == nil {
				out = append(out, p)
			}
		}
	}
	return out
}
