//go:build !windows && !android && !darwin

package daemon

import (
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/Maoyangui/godusevpn/internal/ipc"
)

// 局域网设备发现(网关模式):DHCP 租约给名字,邻居表(ip neigh,没有 ip 命令时读 /proc/net/arp)给在线状态与当前 IP;
// 两边按 MAC 合并,再叠上设置里记的策略。这里只管"看到了什么、给界面显示":策略本身按 MAC 由内核匹配(见 builder),
// 设备换 IP 用不着重新生成配置。

var leaseFiles = []string{"/tmp/dhcp.leases", "/var/lib/misc/dnsmasq.leases", "/var/lib/dhcp/dhcpd.leases", "/var/lib/dhcpd/dhcpd.leases"}

// scanLAN 返回 MAC → 观察到的信息。
func scanLAN() map[string]*seen {
	out := map[string]*seen{}
	now := time.Now().Unix()
	for _, f := range leaseFiles {
		if b, err := os.ReadFile(f); err == nil {
			parseLeases(f, b, now, out)
		}
	}
	var neigh []neighbor
	if b, err := exec.Command("ip", "-4", "neigh", "show").Output(); err == nil {
		neigh = parseIPNeigh(b)
	} else if b, err := os.ReadFile("/proc/net/arp"); err == nil {
		neigh = parseProcARP(b)
	}
	var wan, gw string
	if b, err := os.ReadFile("/proc/net/route"); err == nil {
		wan, gw = parseDefaultRoute(b)
	}
	skipDev, skipIP := lanFilter(wan, gw, lanInterfaces())
	mergeNeighbors(out, neigh, skipDev, skipIP)
	return out
}

// deviceViews 设备列表:设置里记过的(带策略)+ 只在网上看到的。
func (d *Daemon) deviceViews() []ipc.DeviceView {
	s := d.getSettings()
	obs := scanLAN()
	views := make([]ipc.DeviceView, 0, len(obs)+len(s.Devices))
	known := map[string]bool{}
	for _, dev := range s.Devices {
		v := ipc.DeviceView{ID: dev.ID, Name: dev.Name, MAC: dev.MAC, IP: dev.IP, Mode: dev.Mode, Saved: true}
		if o := obs[dev.MAC]; o != nil {
			v.Online = o.online
			if o.ip != "" {
				v.IP = o.ip
			}
			if (v.Name == "" || v.Name == dev.MAC) && o.name != "" {
				v.Name = o.name
			}
		}
		known[dev.MAC] = true
		views = append(views, v)
	}
	for mac, o := range obs {
		if known[mac] || o.ip == "" {
			continue
		}
		views = append(views, ipc.DeviceView{Name: o.name, MAC: mac, IP: o.ip, Online: o.online})
	}
	return views
}

// lanInterfaces 算作局域网的网卡:开着、有 IPv4 地址、不是虚拟网卡(见 virtualIface)。上游网卡也在内。
func lanInterfaces() []string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, in := range ifs {
		if in.Flags&net.FlagUp == 0 || virtualIface(in.Name) {
			continue
		}
		addrs, err := in.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				out = append(out, in.Name)
				break
			}
		}
	}
	return out
}
