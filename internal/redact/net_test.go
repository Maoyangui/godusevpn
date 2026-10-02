package redact

import (
	"strings"
	"testing"
)

// Windows 的 ipconfig /all(中英文两种系统)、route print 的接口列表、Linux 的 ip addr、Android 的网卡列表:
// 公网地址留前缀、MAC 留厂商段、主机名打码;私网 / 链路本地(随机接口号)/ 隧道 / fake-ip 地址、掩码、版本号照旧。
func TestNetIdentity(t *testing.T) {
	in := strings.Join([]string{
		"   Host Name . . . . . . . . . . . . : ALICE-PC",
		"   主机名  . . . . . . . . . . . . . : ALICE-PC",
		"   Physical Address. . . . . . . . . : 00-15-5D-01-02-03",
		"   IPv6 Address. . . . . . . . . . . : 2409:8a55:1234:5678:9abc:def0:1111:2222(Preferred)",
		"   Link-local IPv6 Address . . . . . : fe80::1c2d:3e4f:5a6b:7c8d%12(Preferred)",
		"   Link-local IPv6 Address . . . . . : fe80::215:5dff:fe01:203%13(Preferred)",
		"   IPv4 Address. . . . . . . . . . . : 192.168.1.4(Preferred)",
		"   Subnet Mask . . . . . . . . . . . : 255.255.255.0",
		"   DNS Servers . . . . . . . . . . . : 202.96.128.86",
		"   DHCPv6 Client DUID. . . . . . . . : 00-01-00-01-2A-3B-4C-5D-00-15-5D-01-02-03",
		" 12...00 15 5d 01 02 03 ......Hyper-V Virtual Ethernet Adapter",
		"          0.0.0.0        128.0.0.0      172.19.0.1      172.19.0.1      0",
		"    inet 203.0.113.77/24 brd 203.0.113.255 scope global ppp0",
		"    inet 100.72.3.4/10 scope global wwan0",
		"    inet6 fdfe:dcba:9876::1/126 scope global",
		"    inet 198.18.0.5",
		"OS Version 10.0.19045.3803 at 12:34:56",
	}, "\n")
	got := netIdentity(in, "ALICE-PC")
	for _, gone := range []string{"ALICE-PC", "01-02-03", "1234:5678", "fe01:203", "128.86", "113.77", "113.255", "5d 01 02 03", "4C-5D"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q 还在:\n%s", gone, got)
		}
	}
	for _, keep := range []string{
		"Host Name . . . . . . . . . . . . : <主机名>", "00-15-5D-xx-xx-xx", "2409:8a55:x:x:x:x:x:x(Preferred)",
		"fe80::1c2d:3e4f:5a6b:7c8d%12", "fe80:0:0:0:x:x:x:x%13", "192.168.1.4(Preferred)", "255.255.255.0", "202.96.x.x",
		"00-01-00-xx-xx", "00 15 5d xx xx xx", "128.0.0.0", "172.19.0.1", "inet 203.0.x.x/24 brd 203.0.x.x", "100.72.3.4",
		"fdfe:dcba:9876::1/126", "198.18.0.5", "10.0.19045.3803", "12:34:56",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("缺了 %q:\n%s", keep, got)
		}
	}
}

// Android 网卡列表那行"公网 IPv6 地址数量"照旧:排障要知道这台设备有没有 v6。
func TestNetKeepsV6Count(t *testing.T) {
	in := "    2409:8a55::1/64\n\n公网 IPv6 地址数量: 1(大于 0 说明这台设备有 IPv6)"
	got := netIdentity(in, "")
	if !strings.Contains(got, "2409:8a55:x:x:x:x:x:x/64") || !strings.Contains(got, "公网 IPv6 地址数量: 1") {
		t.Fatalf("got:\n%s", got)
	}
}

// 诊断输出里的内核日志:访问过的域名只留顶级域,公网地址留前缀;fake-ip、私网、隧道地址、出站名照旧。
// 输入照真机 core.log 的几种行写。
func TestDestinations(t *testing.T) {
	for in, want := range map[string]string{
		"dns: lookup succeed for www.google.com: 142.250.72.4 2404:6800:4005:80f::2004": "dns: lookup succeed for *.com: 142.250.x.x 2404:6800:x:x:x:x:x:x",
		"outbound/hysteria2[香港1-大带宽]: outbound connection to api.github.com:443":        "outbound/hysteria2[香港1-大带宽]: outbound connection to *.com:443",
		"inbound/tun[tun-in]: inbound connection to 198.18.0.5:443":                     "inbound/tun[tun-in]: inbound connection to 198.18.0.5:443",
		"inbound/tun[tun-in]: inbound connection from 172.19.0.1:54321":                 "inbound/tun[tun-in]: inbound connection from 172.19.0.1:54321",
		"inbound DNS packet from [fdfe:dcba:9876::1]:53":                                "inbound DNS packet from [fdfe:dcba:9876::1]:53",
		"outbound/direct[direct]: dial tcp 192.168.17.107:7680: i/o timeout":            "outbound/direct[direct]: dial tcp 192.168.17.107:7680: i/o timeout",
		"router: lookup cdn.example.co.uk.: no such host":                               "router: lookup *.uk: no such host",
		"connection: open connection to 104.16.1.2:443 using outbound/auto[auto]":       "connection: open connection to 104.16.x.x:443 using outbound/auto[auto]",
		"sing-box 1.14.0 started (0.12s)":                                               "sing-box 1.14.0 started (0.12s)",
	} {
		if got := Destinations(in); got != want {
			t.Errorf("\n输入 %q\n得到 %q\n应为 %q", in, got, want)
		}
	}
}
