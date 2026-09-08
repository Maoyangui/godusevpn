package settings

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// 规则组:用户自定义分流。每组是一批匹配条件(任一命中即算命中)加一个出口,按列表顺序匹配,
// 排在内置默认规则(私网直连、国内直连、其余走代理)之前;只在"规则"模式下生效,全局 / 直连模式忽略。

const (
	OutProxy  = "proxy"  // 走当前选中的节点
	OutDirect = "direct" // 直连
	OutReject = "reject" // 拒绝(拦截)
	// 其它取值视为节点名(订阅里的 tag)或 "auto";节点不在当前订阅里时回落到 proxy
)

// 条件类型。geosite / geoip 的值是官方规则集的类别名(如 google、netflix、us)。
const (
	RuleDomain        = "domain"
	RuleDomainSuffix  = "domain_suffix"
	RuleDomainKeyword = "domain_keyword"
	RuleDomainRegex   = "domain_regex"
	RuleIPCIDR        = "ip_cidr"
	RulePort          = "port"
	RuleProcess       = "process_name"
	RuleGeosite       = "geosite"
	RuleGeoIP         = "geoip"
)

var RuleTypes = []string{RuleDomain, RuleDomainSuffix, RuleDomainKeyword, RuleDomainRegex, RuleIPCIDR, RulePort, RuleProcess, RuleGeosite, RuleGeoIP}

const (
	MaxRuleGroups     = 50
	MaxRulesPerGroup  = 2000
	MaxRuleValueBytes = 512
)

type Rule struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type RuleGroup struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Outbound string `json:"outbound"`
	Rules    []Rule `json:"rules"`
}

var geoName = regexp.MustCompile(`^[a-z0-9][a-z0-9@!._-]*$`)

// validateRules 规范化并校验规则组:补 id 与名字、拆开逗号 / 空白分隔的多值、逐类型检查。
func (s *Settings) validateRules() error {
	if len(s.RuleGroups) > MaxRuleGroups {
		return fmt.Errorf("规则组最多 %d 个", MaxRuleGroups)
	}
	seen := map[string]bool{}
	for i := range s.RuleGroups {
		g := &s.RuleGroups[i]
		g.ID = strings.TrimSpace(g.ID)
		if g.ID == "" {
			g.ID = NewID()
		}
		if seen[g.ID] {
			return fmt.Errorf("规则组 id 重复: %s", g.ID)
		}
		seen[g.ID] = true
		g.Name = strings.TrimSpace(g.Name)
		if g.Name == "" {
			g.Name = fmt.Sprintf("规则组 %d", i+1)
		}
		g.Outbound = strings.TrimSpace(g.Outbound)
		if g.Outbound == "" {
			g.Outbound = OutProxy
		}
		rules := []Rule{}
		for _, r := range g.Rules {
			for _, v := range splitValues(r.Value) {
				nr, err := normalizeRule(r.Type, v)
				if err != nil {
					return fmt.Errorf("规则组「%s」: %w", g.Name, err)
				}
				rules = append(rules, nr)
			}
		}
		if len(rules) > MaxRulesPerGroup {
			return fmt.Errorf("规则组「%s」条件太多(最多 %d 条)", g.Name, MaxRulesPerGroup)
		}
		g.Rules = rules
	}
	return nil
}

// splitValues 一格里贴了多个值(逗号、分号、换行、空格分隔)就拆成多条。
func splitValues(v string) []string {
	return strings.FieldsFunc(v, func(c rune) bool {
		return c == ',' || c == '，' || c == ';' || c == '；' || c == '\n' || c == '\r' || c == '\t' || c == ' '
	})
}

func normalizeRule(typ, v string) (Rule, error) {
	if len(v) > MaxRuleValueBytes {
		return Rule{}, fmt.Errorf("条件太长: %.30s…", v)
	}
	switch typ {
	case RuleDomain, RuleDomainSuffix, RuleDomainKeyword:
		v = strings.ToLower(strings.Trim(v, "."))
		if typ == RuleDomainSuffix && strings.HasPrefix(v, "*.") { // 常见写法 *.example.com
			v = v[2:]
		}
		if v == "" || strings.ContainsAny(v, "/\\ *") {
			return Rule{}, fmt.Errorf("域名无效: %q", v)
		}
	case RuleDomainRegex:
		if _, err := regexp.Compile(v); err != nil {
			return Rule{}, fmt.Errorf("正则无效: %q(%v)", v, err)
		}
	case RuleIPCIDR:
		if ip := net.ParseIP(v); ip != nil { // 单个 IP 补成 /32 或 /128
			if ip.To4() != nil {
				v += "/32"
			} else {
				v += "/128"
			}
		} else if _, _, err := net.ParseCIDR(v); err != nil {
			return Rule{}, fmt.Errorf("IP 段无效: %q(如 1.2.3.0/24)", v)
		}
	case RulePort:
		a, b, isRange := strings.Cut(v, "-")
		lo, err1 := strconv.Atoi(strings.TrimSpace(a))
		hi, err2 := lo, error(nil)
		if isRange {
			hi, err2 = strconv.Atoi(strings.TrimSpace(b))
		}
		if err1 != nil || err2 != nil || lo < 1 || hi > 65535 || lo > hi {
			return Rule{}, fmt.Errorf("端口无效: %q(如 443 或 6000-7000)", v)
		}
		if isRange {
			v = fmt.Sprintf("%d-%d", lo, hi)
		} else {
			v = strconv.Itoa(lo)
		}
	case RuleProcess:
		if strings.ContainsAny(v, "/\\*?<>|\"") || strings.Contains(v, string(os.PathSeparator)) {
			return Rule{}, fmt.Errorf("进程名无效: %q(只写文件名,如 steam.exe)", v)
		}
	case RuleGeosite, RuleGeoIP:
		v = strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(v, "geosite-"), "geoip-"))
		if !geoName.MatchString(v) {
			return Rule{}, fmt.Errorf("规则集类别无效: %q(如 google、netflix、us)", v)
		}
	default:
		return Rule{}, fmt.Errorf("条件类型无效: %q", typ)
	}
	return Rule{Type: typ, Value: v}, nil
}

// DefaultRules 内置默认规则里可以调的几项。出厂就是"私网直连、国内直连、其余走代理",
// 用户可以逐项改,也可以一键还原(Restore)。这三条永远排在自定义规则组之后。
type DefaultRules struct {
	Private string `json:"private"` // 局域网与私网地址:direct / proxy / reject
	CN      string `json:"cn"`      // 国内域名与 IP(geosite-cn、geoip-cn):direct / proxy / reject
	Final   string `json:"final"`   // 其余流量:proxy / direct
}

// FactoryDefaultRules 出厂值,还原按钮用它。
func FactoryDefaultRules() DefaultRules {
	return DefaultRules{Private: OutDirect, CN: OutDirect, Final: OutProxy}
}

// IsFactory 是否还是出厂状态(界面据此决定要不要显示"已修改")。
func (d DefaultRules) IsFactory() bool { return d == FactoryDefaultRules() }

// normalize 空值补成出厂值:老版本的设置文件里没有这一段。
func (d *DefaultRules) normalize() {
	f := FactoryDefaultRules()
	if d.Private == "" {
		d.Private = f.Private
	}
	if d.CN == "" {
		d.CN = f.CN
	}
	if d.Final == "" {
		d.Final = f.Final
	}
}

func (d DefaultRules) validate() error {
	for _, x := range []struct {
		name, val string
		allow     []string
	}{
		{"私网", d.Private, []string{OutDirect, OutProxy, OutReject}},
		{"国内", d.CN, []string{OutDirect, OutProxy, OutReject}},
		{"其余流量", d.Final, []string{OutProxy, OutDirect}},
	} {
		ok := false
		for _, a := range x.allow {
			if x.val == a {
				ok = true
			}
		}
		if !ok {
			return fmt.Errorf("默认规则「%s」的出口无效: %q(可选 %s)", x.name, x.val, strings.Join(x.allow, " / "))
		}
	}
	return nil
}
