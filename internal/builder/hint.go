package builder

import (
	"encoding/json"
	"net/netip"
	"strings"

	"github.com/Maoyangui/godusevpn/internal/profile"
)

// HintNodes 订阅里的提示节点(按订阅里的顺序):服务器地址只指向本机的出站 —— 回环(127.0.0.0/8、::1、localhost)
// 或未指定地址(0.0.0.0、::)。面板拿它们在节点列表里显示用量、到期、续费:m-ui 的「00-勿选-流量…」是 127.0.0.1:1 的
// ss 占位,也有把「续费请联系…」做成一条指向 127.0.0.1 的线路的,机场的「剩余流量 / 套餐到期」多半也是这样。
//
// 它们连不到任何远端服务器:放进测速组是白测;放进自动选择更糟 —— 测速结果出来之前 urltest 先用排第一的成员,
// 而提示节点常排第一,全局模式下这段时间整机没网。所以不进自动选择与线路组(见 buildConfig);
// 节点列表里照常显示,手动选它和以前一样能选上。
func HintNodes(p *profile.Profile) []string {
	if p == nil {
		return nil
	}
	var out []string
	for i, raw := range p.Outbounds {
		if i >= len(p.Tags) {
			break
		}
		var m struct {
			Server string `json:"server"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if localServer(m.Server) {
			out = append(out, p.Tags[i])
		}
	}
	return out
}

// localServer 地址只指向本机:回环、未指定地址,或 localhost(RFC 6761:localhost 和它的子域名都解析到回环)。
func localServer(server string) bool {
	host := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(server)), ".")
	if host == "" {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsUnspecified()
}
