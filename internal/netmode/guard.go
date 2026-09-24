package netmode

// 「全局禁直连」的闸。
//
// 全局模式下用户想连着(没点断开)的时候,除了隧道自己(节点服务器的连接、订阅刷新的回退——都是本服务
// 进程发出的)、局域网 / 私网、回环,任何流量都不允许绕过隧道直连:隧道没起来、内核在重启、节点不通、
// 内核崩了在退避重试,统统只能等,不能漏。这道闸是内核外面的东西(系统防火墙),内核死活都不影响它;
// 点断开、切到规则 / 直连模式、关掉开关,闸立刻撤。
//
// 各平台的做法不一样,接口一样:
//   - Windows:WFP 动态会话里的一组过滤器(见 wfp 包),进程一退出规则自动消失
//   - macOS:pf 里 com.apple/godusevpn 锚点的一组规则
//   - Linux:nftables 的 inet godusevpn_guard 表
//   - Android:不在这里做——VPN 接口本身就是闸,内核重启时不关它就行(见 mobile 包)

// GuardSpec 闸的参数。
type GuardSpec struct {
	TunName  string // 隧道网卡名
	TunAddr4 string // 隧道 v4 地址(不带前缀长度):经隧道出去的包源地址就是它
	TunAddr6 string // 隧道 v6 地址
	LAN      bool   // 放行局域网 / 私网(打印机、NAS、路由器后台——不出网,不算漏)
	Gateway  bool   // Linux 网关模式:经本机转发的局域网流量也只许走隧道
	// SelfPath 放行的服务 exe 路径(Windows):空 = 当前进程。只有升级前的离线预装(guardfix.Arm)
	// 会填 —— 那时跑的是安装器解到临时目录的新版 exe,要放行的却是安装后那个路径上的服务。
	SelfPath string
}

// 私网 / 本地段:这些目标不出网,闸放行它们。
var (
	privateV4 = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "224.0.0.0/4", "255.255.255.255/32"}
	privateV6 = []string{"fe80::/10", "ff00::/8", "fc00::/7"}
)
