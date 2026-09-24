package ipc

import (
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/state"
)

// 视图类型放在这里而不是 daemon:命令行与托盘客户端只需要这些结构,不该把内嵌内核一起编进去。

// ProfileView 一条订阅:设置里的名字与地址 + 缓存里的节点数、用量、更新时间。
type ProfileView struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	URL       string        `json:"url"`
	Active    bool          `json:"active"`
	Title     string        `json:"title"`     // 面板给的标题(Profile-Title)
	FetchedAt int64         `json:"fetchedAt"` // 0 = 还没拉到过
	NodeCount int           `json:"nodeCount"`
	Tags      []string      `json:"tags"`
	WebPage   string        `json:"webPage,omitempty"` // 面板给的「选购 / 续费」地址,空 = 没配
	Usage     profile.Usage `json:"usage"`
	Error     string        `json:"error,omitempty"` // 最近一次拉取失败的原因
}

// DeviceView 局域网设备(网关模式):设置里记过的带 Saved 与策略,只在网上看到的 Saved 为假。
type DeviceView struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	MAC    string `json:"mac"`
	IP     string `json:"ip"`
	Online bool   `json:"online"`
	Mode   string `json:"mode"` // "" 跟随规则 | proxy | direct | reject
	Saved  bool   `json:"saved"`
}

// ClashInfo 内核 Clash API 的连接信息;Running 为假时端口未监听。
type ClashInfo struct {
	Port    int    `json:"port"`
	Secret  string `json:"secret"`
	Running bool   `json:"running"`
}

type StateView struct {
	Version    string            `json:"version"`
	Protocol   int               `json:"protocol"`
	State      state.Snapshot    `json:"state"`
	Mode       string            `json:"mode"`              // rule / global / direct
	Node       string            `json:"node"`              // proxy 组当前项
	AutoNow    string            `json:"autoNow,omitempty"` // 自动选择组当前落在哪个节点(内核在跑时才有)
	Nodes      []string          `json:"nodes"`
	Delays     map[string]int    `json:"delays,omitempty"`     // 最近一次全节点测速(节点 → 毫秒,-1 不通);内核没跑时界面靠它显示
	Ping       int               `json:"ping,omitempty"`       // 当前节点最近一次测得的延迟(毫秒):连上后立刻测一次,之后每次健康检查顺带更新
	ExitIP     string            `json:"exitIp,omitempty"`     // 经当前节点出去时对外露出的地址;查不到就空着
	ExitLoc    string            `json:"exitLoc,omitempty"`    // 出口所在国家的两位代码(US / ES …)
	ExitCity   string            `json:"exitCity,omitempty"`   // 出口所在城市(英文,接口给什么就是什么)
	ExitRegion string            `json:"exitRegion,omitempty"` // 出口所在一级行政区
	ExitISP    string            `json:"exitIsp,omitempty"`    // 出口那条线路的运营商 / 机房
	Uptime     int64             `json:"uptime"`
	Guard      string            `json:"guard,omitempty"`      // 全局禁直连的闸:on = 开着;空 = 没开(开关关了、不是全局模式、或没在连)
	GuardError string            `json:"guardError,omitempty"` // 闸该开却没开成(或隧道网卡放行失败)的原因
	NICLost    string            `json:"nicLost,omitempty"`    // 有几张网卡动手前的 IPv6 状态丢了、可能还关着;用户点"知道了"之前一直带着
	Profile    *ProfileView      `json:"profile,omitempty"`    // 当前订阅
	Profiles   []ProfileView     `json:"profiles"`             // 全部订阅
	Settings   settings.Settings `json:"settings"`
	// MissingRuleSets 本地还没有、因此这一轮被摘掉的规则集标签。用到它们的规则组暂时不生效,
	// 界面要照实说一句 —— 规则开着却不起作用,用户是看不出来的。连上之后守护进程会自动补下来。
	MissingRuleSets []string `json:"missingRuleSets,omitempty"`
	// Tunnel 隧道会话的看护记录(hysteria2 / tuic 这类一条 QUIC 会话承载全部流量的节点)。
	// 出事时凭它一眼分清是"会话废了被重建"还是别的:以前这些事一点痕迹都不留。
	Tunnel *TunnelView `json:"tunnel,omitempty"`
}

// TunnelView 隧道会话的看护记录,本次服务运行期间累计。
type TunnelView struct {
	Rebuilds   int    `json:"rebuilds"`             // 当前在用的节点的会话被拆掉重建了几次(网络变化 + 判废);别的出站不计
	Sick       int    `json:"sick"`                 // 其中被看护判定为"活着却不投递"而拆掉的次数(同样只计在用的节点)
	LastAt     int64  `json:"lastAt,omitempty"`     // 最近一次重建的时间
	LastNode   string `json:"lastNode,omitempty"`   // 最近一次重建的是哪个节点
	LastReason string `json:"lastReason,omitempty"` // 最近一次重建的原因
}
