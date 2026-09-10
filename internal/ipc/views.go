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
	Profile    *ProfileView      `json:"profile,omitempty"` // 当前订阅
	Profiles   []ProfileView     `json:"profiles"`          // 全部订阅
	Settings   settings.Settings `json:"settings"`
}
