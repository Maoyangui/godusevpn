package redact

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"runtime"
	"strings"
)

// 诊断包里的系统网络信息(ipconfig、route、ip addr、ifconfig、网卡列表)带着这台设备真实的网络身份:
// 运营商给的公网 IPv4 / IPv6(对得上具体宽带线路)、网卡 MAC、主机名。诊断包是"可以直接发给客服"的,这些要打码;
// 排障要看的网卡名、私网 / 链路本地 / 隧道地址、"有没有公网 v6"照样看得出。
var (
	ipv4Addr  = regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`)
	ipv6Addr  = regexp.MustCompile(`[0-9A-Fa-f]{1,4}(?::[0-9A-Fa-f]{0,4}){2,7}`)
	macSpaced = regexp.MustCompile(`[0-9A-Fa-f]{2}(?: [0-9A-Fa-f]{2}){5}`) // route print 的接口列表这么写
	cgnat     = mustCIDR("100.64.0.0/10")
	fakeIP    = mustCIDR("198.18.0.0/15")
)

// Net 公网 IPv4 留前两段(203.0.x.x),公网 IPv6 留前 32 位(2409:8a55:x:x:x:x:x:x),
// 接口号由 MAC 算出的 IPv6(EUI-64,含 ff:fe)只留 /64 前缀,MAC 留厂商段,本机主机名换成 <主机名>。
// 只给系统命令的输出用:日志和节点列表里的公网地址是节点与访问目标,不是用户自己的。
func Net(s string) string {
	host := ""
	// Linux / Android 收集的输出里没有主机名;软路由的默认主机名 OpenWrt 同时是发行版名,盖了反而把 openwrt_release 盖坏。
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		host, _ = os.Hostname()
	}
	return netIdentity(s, host)
}

func netIdentity(s, host string) string {
	if host = strings.TrimSpace(host); len(host) >= 2 && !strings.EqualFold(host, "localhost") {
		s = replaceBounded(s, regexp.MustCompile(`(?i)`+regexp.QuoteMeta(host)), wordEdge, func(string) string { return "<主机名>" })
	}
	s = replaceBounded(s, ipv6Addr, v6Edge, maskV6)
	s = replaceBounded(s, ipv4Addr, v4Edge, maskV4)
	s = replaceBounded(s, macSpaced, wordEdge, maskMAC)
	return replaceBounded(s, macAddr, macEdge, maskMAC)
}

func maskV4(s string) string {
	ip := net.ParseIP(s).To4()
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || cgnat.Contains(ip) || fakeIP.Contains(ip) || ip[0] >= 240 {
		return s
	}
	if ones, bits := net.IPMask(ip).Size(); bits == 32 && ones > 0 {
		return s // 路由表里的掩码(128.0.0.0、192.0.0.0)不是谁的地址
	}
	p := strings.Split(s, ".")
	return p[0] + "." + p[1] + ".x.x"
}

func maskV6(s string) string {
	ip := net.ParseIP(s)
	if ip == nil || ip.To4() != nil {
		return s
	}
	switch {
	case ip.IsGlobalUnicast() && !ip.IsPrivate():
		return v6Prefix(ip, 2)
	case ip[11] == 0xff && ip[12] == 0xfe:
		return v6Prefix(ip, 4)
	}
	return s
}

func v6Prefix(ip net.IP, keep int) string {
	var b strings.Builder
	for i := 0; i < 8; i++ {
		if i > 0 {
			b.WriteByte(':')
		}
		if i < keep {
			fmt.Fprintf(&b, "%x", uint16(ip[2*i])<<8|uint16(ip[2*i+1]))
		} else {
			b.WriteByte('x')
		}
	}
	return b.String()
}

// v4Edge 前面不能连着字母数字或点(版本号 10.0.19045.1 不是地址),后面不能再跟数字或"点加数字"。
func v4Edge(s string, i, j int) bool {
	if i > 0 && (isAlnum(s[i-1]) || s[i-1] == '.') {
		return false
	}
	if j < len(s) && (isAlnum(s[j]) || s[j] == '.' && j+1 < len(s) && s[j+1] >= '0' && s[j+1] <= '9') {
		return false
	}
	return true
}

// v6Edge 前后不能再连着十六进制、冒号;后面跟"点加数字"的是内嵌 IPv4 的写法,交给 IPv4 那条处理。
func v6Edge(s string, i, j int) bool {
	if i > 0 && (isAlnum(s[i-1]) || s[i-1] == ':' || s[i-1] == '.') {
		return false
	}
	if j < len(s) && (isAlnum(s[j]) || s[j] == ':' || s[j] == '.' && j+1 < len(s) && s[j+1] >= '0' && s[j+1] <= '9') {
		return false
	}
	return true
}

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// 诊断输出里的内核日志:每条连接的目标(域名、解析结果、公网地址)合起来就是一份浏览记录。
// 域名只留顶级域(*.com),公网地址同 Net 打成前缀;假地址、私网、隧道地址照旧 —— 排障要看的
// "连没连上、走哪个出站、卡在哪一步"不受影响。日志文件本身不动(日志目录只有控制用户读得到)。
var domainName = regexp.MustCompile(`(?i)(?:[a-z0-9_](?:[a-z0-9_-]*[a-z0-9])?\.)+[a-z]{2,63}\.?`)

// Destinations 打码内核日志里的访问目标。
func Destinations(s string) string {
	s = replaceBounded(s, domainName, hostEdge, maskDomain)
	return netIdentity(s, "")
}

func maskDomain(d string) string {
	d = strings.TrimSuffix(d, ".")
	return "*." + d[strings.LastIndexByte(d, '.')+1:]
}

// hostEdge 域名前后不能再连着能组成主机名的字符(免得把长串里的一截当成域名)。
func hostEdge(s string, i, j int) bool {
	part := func(c byte) bool { return isAlnum(c) || c == '-' || c == '_' || c == '.' }
	return (i == 0 || !part(s[i-1])) && (j == len(s) || !part(s[j]))
}
