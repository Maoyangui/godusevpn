package netmode

import "strings"

// 「防流量逃逸」的闸。
//
// 用户想连着(没点断开)的时候,该走隧道的流量不允许从本机网络出去:隧道没起来、内核在重启、节点不通、
// 内核崩了在退避重试、设备刚开机,统统只能等,不能漏。这道闸是内核外面的东西(系统防火墙),内核死活都不影响它;
// 点断开、切到直连模式、关掉开关,闸立刻撤,撤完系统防火墙回到原样(闸是自己单独的一组对象,不碰别人的规则)。
//
// 全局模式:除了隧道自己(节点服务器的连接、订阅刷新的回退——都是本服务进程发出的)、局域网 / 私网、回环,全拦。
// 规则模式:内核好好的时候和全局一样(直连由内核按规则发出,是本服务的流量);内核不在的时候防火墙照着规则放行,
// 直连规则的地址段(默认是局域网和国内 IP)照常通,其余只能等(见 builder.GuardRules)。
//
// 各平台的做法不一样,接口一样:
//   - Windows:WFP 里一组持久(PERSISTENT)过滤器,外加一组开机(BOOTTIME)过滤器管开机到 BFE 启动那几秒;
//     进程退出、被杀、崩溃、重启都还在,只有明确撤闸才删(见 wfp 包)
//   - macOS:pf 里 com.apple/godusevpn 锚点的一组规则
//   - Linux:nftables 的 inet godusevpn_guard 表
//   - Android:不在这里做——VPN 接口本身就是闸,内核重启时不关它就行(见 mobile 包)

// GuardSpec 闸的参数。
type GuardSpec struct {
	TunName  string // 隧道网卡名
	TunAddr4 string // 隧道 v4 地址(不带前缀长度):经隧道出去的包源地址就是它
	TunAddr6 string // 隧道 v6 地址
	LAN      bool   // 放行局域网 / 私网(打印机、NAS、路由器后台——不出网,不算漏)
	Gateway  bool   // Linux 网关模式:内核出站带的是 auto_redirect 的标记,闸要认它(转发的流量不论哪种模式都只许走隧道)

	// 规则模式那几项(全局模式都是零值)。见 builder.GuardRules。都是字符串:规格要能直接比较("设置变了要不要重装")。
	Rule        bool   // 规则模式:直连规则的地址段照常放行,入站不拦,其余只许走隧道
	Direct      string // 放行的地址段,逗号分隔
	TunnelPorts string // 规则明确走隧道的端口,逗号分隔,"443" 或 "8000-9000"
	DevBlock    string // 网关模式:设成走代理 / 拒绝的设备(MAC 或 IP),逗号分隔
	DevDirect   string // 网关模式:设成直连的设备,逗号分隔
}

// splitList 按逗号拆开;空串是空列表(strings.Split 会给出一个空元素)。
func splitList(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// directFamilies 放行段按地址族分开。
func (s GuardSpec) directFamilies() (v4, v6 []string) {
	for _, p := range splitList(s.Direct) {
		if strings.Contains(p, ":") {
			v6 = append(v6, p)
		} else {
			v4 = append(v4, p)
		}
	}
	return v4, v6
}

// 私网 / 本地段:这些目标不出网,闸放行它们。
var (
	privateV4 = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "224.0.0.0/4", "255.255.255.255/32"}
	privateV6 = []string{"fe80::/10", "ff00::/8", "fc00::/7"}
)
