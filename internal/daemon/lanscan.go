package daemon

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 局域网设备发现用到的解析与判断:都是纯函数,哪个平台都能测,真正读系统的部分在 devices_linux.go。

// seen 观察到的一台设备。
type seen struct {
	ip, name string
	online   bool
}

// neighbor 邻居表的一行。
type neighbor struct {
	ip, dev, mac string
	online       bool
}

// parseLeases 把一份 DHCP 租约读进 out(MAC → 设备)。按文件名认格式:dhcpd.leases 是 ISC dhcpd 的,其余是 dnsmasq 的
// (OpenWrt 的 /tmp/dhcp.leases 也是);已经到期的租约不算,否则早就走了的设备和它的旧地址会一直挂在列表里。
func parseLeases(path string, b []byte, now int64, out map[string]*seen) {
	if strings.HasSuffix(path, "dhcpd.leases") {
		parseISCLeases(b, now, out)
		return
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		// dnsmasq: 到期时间 MAC IP 主机名 客户端id(到期时间 0 = 不到期)
		fs := strings.Fields(sc.Text())
		if len(fs) < 4 {
			continue
		}
		mac := settings.NormalizeMAC(fs[1])
		exp, err := strconv.ParseInt(fs[0], 10, 64)
		if mac == "" || err != nil || (exp != 0 && exp < now) || net.ParseIP(fs[2]) == nil || strings.Contains(fs[2], ":") {
			continue
		}
		name := fs[3]
		if name == "*" {
			name = ""
		}
		out[mac] = &seen{ip: fs[2], name: name}
	}
}

// parseISCLeases ISC dhcpd 的租约文件:一个地址一段,同一个地址越靠后的越新,所以按地址留最后一段再算。
//
//	lease 192.168.1.10 {
//	  ends 3 2026/10/30 14:00:00;
//	  binding state active;
//	  hardware ethernet aa:bb:cc:dd:ee:ff;
//	  client-hostname "tv";
//	}
func parseISCLeases(b []byte, now int64, out map[string]*seen) {
	type lease struct {
		ip, mac, name string
		active        bool
		ended         bool
	}
	byIP := map[string]*lease{}
	var order []string
	var cur *lease
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		fs := strings.Fields(strings.TrimSuffix(line, ";"))
		switch {
		case len(fs) == 3 && fs[0] == "lease" && fs[2] == "{":
			cur = &lease{ip: fs[1]}
		case cur == nil || len(fs) == 0:
		case fs[0] == "}":
			if _, ok := byIP[cur.ip]; !ok {
				order = append(order, cur.ip)
			}
			byIP[cur.ip] = cur
			cur = nil
		case len(fs) == 3 && fs[0] == "binding" && fs[1] == "state":
			cur.active = fs[2] == "active"
		case len(fs) == 3 && fs[0] == "hardware" && fs[1] == "ethernet":
			cur.mac = settings.NormalizeMAC(fs[2])
		case len(fs) == 2 && fs[0] == "client-hostname":
			cur.name = strings.Trim(fs[1], `"`)
		case len(fs) >= 3 && fs[0] == "ends" && fs[1] == "epoch":
			if at, err := strconv.ParseInt(fs[2], 10, 64); err == nil && at < now {
				cur.ended = true
			}
		case len(fs) == 4 && fs[0] == "ends":
			if at, err := time.Parse("2006/01/02 15:04:05", fs[2]+" "+fs[3]); err == nil && at.Unix() < now {
				cur.ended = true
			}
		}
	}
	for _, ip := range order {
		if l := byIP[ip]; l.active && !l.ended && l.mac != "" && net.ParseIP(ip) != nil {
			out[l.mac] = &seen{ip: ip, name: l.name}
		}
	}
}

// parseIPNeigh 解析 `ip -4 neigh show`:10.99.0.50 dev eth1 lladdr 02:42:0a:63:00:32 REACHABLE。
// 没解析出 MAC 的(FAILED、INCOMPLETE)不要。
func parseIPNeigh(b []byte) []neighbor {
	var out []neighbor
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 5 {
			continue
		}
		n := neighbor{ip: fs[0]}
		for i := 1; i+1 < len(fs); i++ {
			switch fs[i] {
			case "lladdr":
				n.mac = settings.NormalizeMAC(fs[i+1])
			case "dev":
				n.dev = fs[i+1]
			}
		}
		if n.mac == "" {
			continue
		}
		switch fs[len(fs)-1] {
		case "REACHABLE", "STALE", "DELAY", "PROBE":
			n.online = true
		}
		out = append(out, n)
	}
	return out
}

// parseProcARP 解析 /proc/net/arp(极简系统没有 ip 命令时用):
//
//	IP address       HW type     Flags       HW address            Mask     Device
//	192.168.1.1      0x1         0x2         aa:bb:cc:dd:ee:ff     *        eth0
//
// Flags 0x2 = 解析成功;这张表分不出 STALE 和 REACHABLE,解析成功的都算在线。
func parseProcARP(b []byte) []neighbor {
	var out []neighbor
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 6 || fs[0] == "IP" {
			continue
		}
		flags, err := strconv.ParseInt(strings.TrimPrefix(fs[2], "0x"), 16, 32)
		mac := settings.NormalizeMAC(fs[3])
		if err != nil || flags&2 == 0 || mac == "" || mac == "00:00:00:00:00:00" {
			continue
		}
		out = append(out, neighbor{ip: fs[0], dev: fs[5], mac: mac, online: true})
	}
	return out
}

// parseDefaultRoute 从 /proc/net/route 找默认路由:返回走的网卡和网关地址(点对点链路如 PPPoE 没有网关,返回空)。
// 有多条取跃点数最小的。地址是小端的十六进制。
func parseDefaultRoute(b []byte) (dev, gw string) {
	best := int64(-1)
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		// Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT
		fs := strings.Fields(sc.Text())
		if len(fs) < 8 || fs[1] != "00000000" || fs[7] != "00000000" {
			continue
		}
		metric, err := strconv.ParseInt(fs[6], 10, 64)
		if err != nil || (best >= 0 && metric >= best) {
			continue
		}
		best, dev, gw = metric, fs[0], ""
		if g, err := hex.DecodeString(fs[2]); err == nil && len(g) == 4 && fs[2] != "00000000" {
			gw = net.IPv4(g[3], g[2], g[1], g[0]).String()
		}
	}
	return dev, gw
}

// virtualIface 这张网卡上的邻居不是局域网设备:回环、本隧道、容器和虚拟机的网桥、别的 VPN。
// 网桥只认 Docker 自动起的 br-<12 位十六进制>,OpenWrt 的 br-lan 才是真正的局域网。
func virtualIface(name string) bool {
	if name == "lo" || name == builder.TunName {
		return true
	}
	for _, p := range []string{"docker", "veth", "virbr", "vnet", "lxc", "lxd", "cni", "flannel", "cali", "wg", "tun", "tap", "tailscale", "zt", "podman"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	if rest, ok := strings.CutPrefix(name, "br-"); ok && len(rest) == 12 {
		if _, err := hex.DecodeString(rest); err == nil {
			return true
		}
	}
	return false
}

// lanFilter 哪些邻居不算局域网设备。上游网卡上的(运营商网关等)一般不算 —— 但旁路由只有一张网卡,上游和局域网是同一张,
// 按网卡排除就一台设备都看不到了,所以只有还别的真网卡时才排除上游网卡,否则只排除上游网关这一个地址。
// lans 是有 IPv4 地址的真网卡(lanInterfaces)。
func lanFilter(wan, gw string, lans []string) (skipDev func(string) bool, skipIP string) {
	multi := false
	for _, n := range lans {
		if n != wan {
			multi = true
		}
	}
	if !multi {
		skipIP = gw
	}
	return func(dev string) bool { return virtualIface(dev) || (multi && dev == wan) }, skipIP
}

// mergeNeighbors 把邻居表叠到租约上:租约给名字,邻居表给在线状态与当前地址。
func mergeNeighbors(out map[string]*seen, neigh []neighbor, skipDev func(string) bool, skipIP string) {
	for _, n := range neigh {
		if skipDev(n.dev) || n.ip == skipIP {
			continue
		}
		if s, ok := out[n.mac]; ok {
			if n.online {
				s.ip, s.online = n.ip, true
			}
		} else {
			out[n.mac] = &seen{ip: n.ip, online: n.online}
		}
	}
}
