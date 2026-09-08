//go:build !windows && !android

package daemon

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 局域网设备发现(网关模式):DHCP 租约(dnsmasq 的 /tmp/dhcp.leases 或 /var/lib/misc/dnsmasq.leases)给名字,
// 邻居表(ip neigh)给在线状态与当前 IP;两边按 MAC 合并,再叠上设置里记的策略。

var leaseFiles = []string{"/tmp/dhcp.leases", "/var/lib/misc/dnsmasq.leases", "/var/lib/dhcp/dhcpd.leases"}

type seen struct {
	ip, name string
	online   bool
	expire   int64
}

// scanLAN 返回 MAC → 观察到的信息。
func scanLAN(tunName string) map[string]*seen {
	out := map[string]*seen{}
	for _, f := range leaseFiles {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			// dnsmasq: 到期时间 MAC IP 主机名 客户端id
			fs := strings.Fields(sc.Text())
			if len(fs) < 4 {
				continue
			}
			mac := settings.NormalizeMAC(fs[1])
			if mac == "" {
				continue
			}
			exp, _ := strconv.ParseInt(fs[0], 10, 64)
			name := fs[3]
			if name == "*" {
				name = ""
			}
			out[mac] = &seen{ip: fs[2], name: name, expire: exp}
		}
	}
	wan := wanInterface()
	if b, err := exec.Command("ip", "-4", "neigh", "show").Output(); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			// 10.99.0.50 dev eth1 lladdr 02:42:0a:63:00:32 REACHABLE
			fs := strings.Fields(sc.Text())
			if len(fs) < 5 {
				continue
			}
			var mac, dev, state string
			for i := 1; i+1 < len(fs); i++ {
				switch fs[i] {
				case "lladdr":
					mac = settings.NormalizeMAC(fs[i+1])
				case "dev":
					dev = fs[i+1]
				}
			}
			state = fs[len(fs)-1]
			if mac == "" || dev == tunName || dev == "lo" || (wan != "" && dev == wan) { // 上游网卡上的邻居(运营商网关等)不是局域网设备
				continue
			}
			online := state == "REACHABLE" || state == "STALE" || state == "DELAY" || state == "PROBE"
			if s, ok := out[mac]; ok {
				if online {
					s.ip, s.online = fs[0], true
				}
			} else {
				out[mac] = &seen{ip: fs[0], online: online}
			}
		}
	}
	return out
}

// wanInterface 默认路由走的网卡(上游);拿不到返回空。
func wanInterface() string {
	b, err := exec.Command("ip", "-4", "route", "show", "default").Output()
	if err != nil {
		return ""
	}
	fs := strings.Fields(string(b))
	for i := 0; i+1 < len(fs); i++ {
		if fs[i] == "dev" {
			return fs[i+1]
		}
	}
	return ""
}

// deviceViews 设备列表:设置里记过的(带策略)+ 只在网上看到的。
func (d *Daemon) deviceViews() []ipc.DeviceView {
	s := d.getSettings()
	obs := scanLAN(tunName())
	views := make([]ipc.DeviceView, 0, len(obs)+len(s.Devices))
	known := map[string]bool{}
	for _, dev := range s.Devices {
		v := ipc.DeviceView{ID: dev.ID, Name: dev.Name, MAC: dev.MAC, IP: dev.IP, Mode: dev.Mode, Saved: true}
		if o := obs[dev.MAC]; o != nil {
			v.Online = o.online
			if o.ip != "" {
				v.IP = o.ip
			}
			if v.Name == "" || v.Name == dev.MAC {
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

// resolveDeviceIPs 生成配置前把设置里设备的当前 IP 填上(策略按 IP 生效)。
func (d *Daemon) resolveDeviceIPs(s *settings.Settings) {
	obs := scanLAN(tunName())
	for i := range s.Devices {
		if o := obs[s.Devices[i].MAC]; o != nil && o.ip != "" {
			s.Devices[i].IP = o.ip
		}
	}
}

// lanInterfaces 网关模式下算作局域网的网卡:排除 TUN、回环、docker 之类的虚拟口;拿不到就返回空(= 全部非 TUN 网卡)。
func lanInterfaces() []string {
	b, err := exec.Command("ip", "-o", "link", "show", "up").Output()
	if err != nil {
		return nil
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 2 {
			continue
		}
		name := strings.TrimSuffix(fs[1], ":")
		if i := strings.IndexByte(name, '@'); i > 0 {
			name = name[:i]
		}
		if name == "lo" || name == tunName() || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "wg") {
			continue
		}
		out = append(out, name)
	}
	return out
}

// deviceRescan 网关模式下每隔一段时间看设备 IP 有没有变,变了就重新应用配置(策略按 IP 生效)。
func (d *Daemon) deviceRescan() {
	s := d.getSettings()
	if s.NetMode != settings.NetGateway || !d.core.Running() {
		return
	}
	before := deviceIPKey(s)
	d.resolveDeviceIPs(&s)
	if deviceIPKey(s) != before {
		d.logf("局域网设备地址有变化,重新应用配置")
		d.mu.Lock()
		for i := range d.settings.Devices {
			for _, nd := range s.Devices {
				if d.settings.Devices[i].ID == nd.ID {
					d.settings.Devices[i].IP = nd.IP
				}
			}
		}
		d.mu.Unlock()
		d.machine.Restart()
	}
}

func deviceIPKey(s settings.Settings) string {
	var b strings.Builder
	for _, dev := range s.Devices {
		if dev.Mode != "" {
			b.WriteString(dev.MAC + "=" + dev.IP + ";")
		}
	}
	return b.String()
}

var _ = time.Second
