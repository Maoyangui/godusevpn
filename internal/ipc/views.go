package ipc

import (
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/state"
)

// 视图类型放在这里而不是 daemon:命令行与托盘客户端只需要这些结构,不该把内嵌内核一起编进去。

type ProfileView struct {
	URL       string        `json:"url"`
	Title     string        `json:"title"`
	FetchedAt int64         `json:"fetchedAt"`
	NodeCount int           `json:"nodeCount"`
	Tags      []string      `json:"tags"`
	Usage     profile.Usage `json:"usage"`
}

type StateView struct {
	Version  string            `json:"version"`
	Protocol int               `json:"protocol"`
	State    state.Snapshot    `json:"state"`
	Mode     string            `json:"mode"` // rule / global / direct
	Node     string            `json:"node"` // proxy 组当前项
	Nodes    []string          `json:"nodes"`
	Uptime   int64             `json:"uptime"`
	Profile  *ProfileView      `json:"profile,omitempty"`
	Settings settings.Settings `json:"settings"`
}
