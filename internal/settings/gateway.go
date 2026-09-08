package settings

import (
	"fmt"
	"net"
	"strings"
)

// 网关模式(Linux 软路由):本机不只代理自己的流量,还代理经它转发的局域网设备。
//
//	NetMode    local(默认,只代理本机)| gateway(网关)
//	LANSubnets 网关模式下视为局域网的网段;空 = 自动取本机非 TUN 网卡上的私网段
//	DNSHijack  网关模式下把局域网设备发往本机 53 端口的查询劫持进内核(fake-ip、防泄漏);dnsmasq 只留 DHCP
//	Devices    局域网设备策略:按 MAC 记,按当前 IP 生效;mode 为空 = 跟随规则,proxy / direct / reject = 强制
const (
	NetLocal   = "local"
	NetGateway = "gateway"
)

type Device struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	MAC  string `json:"mac"`
	IP   string `json:"ip,omitempty"` // 最近一次看到的 IP(MAC 为空时按这个匹配)
	Mode string `json:"mode"`         // "" | proxy | direct | reject
}

func (s *Settings) validateGateway() error {
	s.NetMode = strings.TrimSpace(strings.ToLower(s.NetMode))
	switch s.NetMode {
	case "":
		s.NetMode = NetLocal
	case NetLocal, NetGateway:
	default:
		return fmt.Errorf("网络模式无效: %q(local / gateway)", s.NetMode)
	}
	var subnets []string
	for _, c := range s.LANSubnets {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(c); err != nil {
			return fmt.Errorf("局域网网段无效: %q(如 192.168.1.0/24)", c)
		}
		subnets = append(subnets, c)
	}
	s.LANSubnets = subnets
	seen := map[string]bool{}
	var devs []Device
	for _, d := range s.Devices {
		d.ID = strings.TrimSpace(d.ID)
		d.MAC = NormalizeMAC(d.MAC)
		d.IP = strings.TrimSpace(d.IP)
		d.Name = strings.TrimSpace(d.Name)
		d.Mode = strings.TrimSpace(strings.ToLower(d.Mode))
		if d.MAC == "" && (d.IP == "" || net.ParseIP(d.IP) == nil) {
			return fmt.Errorf("设备「%s」缺少 MAC 或 IP", d.Name)
		}
		if d.ID == "" {
			d.ID = NewID()
		}
		if seen[d.ID] {
			return fmt.Errorf("设备 id 重复: %s", d.ID)
		}
		seen[d.ID] = true
		switch d.Mode {
		case "", "proxy", "direct", "reject":
		default:
			return fmt.Errorf("设备「%s」的策略无效: %q", d.Name, d.Mode)
		}
		if d.Name == "" {
			if d.MAC != "" {
				d.Name = d.MAC
			} else {
				d.Name = d.IP
			}
		}
		devs = append(devs, d)
	}
	s.Devices = devs
	return nil
}

// NormalizeMAC 统一成小写冒号分隔;空或不像 MAC 的返回空串。
func NormalizeMAC(m string) string {
	m = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(m, "-", ":")))
	if m == "" {
		return ""
	}
	if hw, err := net.ParseMAC(m); err == nil {
		return hw.String()
	}
	return ""
}
