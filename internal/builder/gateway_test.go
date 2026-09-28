package builder

import (
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 网关模式:TUN 开 auto_redirect;设备策略按来源 IP 渲染成规则,放在模式分支之前;本机模式什么都不加。
func TestGatewayMode(t *testing.T) {
	s := settings.Default()
	s.NetMode = settings.NetGateway
	s.Devices = []settings.Device{
		{Name: "电视", MAC: "aa:bb:cc:dd:ee:01", IP: "10.99.0.20", Mode: "direct"},
		{Name: "手机", MAC: "aa:bb:cc:dd:ee:02", IP: "10.99.0.21", Mode: "proxy"},
		{Name: "小孩平板", MAC: "aa:bb:cc:dd:ee:03", IP: "10.99.0.22", Mode: "reject"},
		{Name: "跟随规则", MAC: "aa:bb:cc:dd:ee:04", IP: "10.99.0.23"},
		{Name: "没在线", MAC: "aa:bb:cc:dd:ee:05", Mode: "direct"},
	}
	c, raw := build(t, s)
	if !strings.Contains(raw, `"auto_redirect": true`) {
		t.Fatal("网关模式应开 auto_redirect")
	}
	var iDirect, iProxy, iReject, iMode = -1, -1, -1, -1
	for i, r := range c.Route.Rules {
		src, _ := r["source_ip_cidr"].([]any)
		switch {
		case len(src) > 0 && r["outbound"] == "direct":
			iDirect = i
			if src[0] != "10.99.0.20/32" {
				t.Fatalf("直连设备地址不对: %v", src)
			}
		case len(src) > 0 && r["outbound"] == "proxy":
			iProxy = i
		case len(src) > 0 && r["action"] == "reject":
			iReject = i
		case r["clash_mode"] == "Direct":
			iMode = i
		}
	}
	if iDirect < 0 || iProxy < 0 || iReject < 0 {
		t.Fatalf("三种设备策略都应有规则: %d %d %d", iDirect, iProxy, iReject)
	}
	if iMode < 0 || iDirect > iMode || iProxy > iMode || iReject > iMode {
		t.Fatal("设备规则应排在模式分支之前")
	}
	if strings.Contains(raw, "10.99.0.23") {
		t.Fatal("跟随规则的设备不该出现")
	}

	s.NetMode = settings.NetLocal
	_, raw = build(t, s)
	if strings.Contains(raw, "auto_redirect") || strings.Contains(raw, "source_ip_cidr") {
		t.Fatal("本机模式不该有网关相关配置")
	}
}

// 关 IPv6 时设备策略同样要守"全链路禁 IPv6":强制代理的设备,域名要先经远程 DNS 按 ipv4_only 解析再交给节点
// (否则节点自己解析、解析出 AAAA 就走 IPv6 出去);直连的设备写 IPv6 字面量也要被拒,不能从路由器直接出 v6。
func TestGatewayDevicesNoIPv6(t *testing.T) {
	s := settings.Default()
	s.NetMode = settings.NetGateway
	s.Devices = []settings.Device{
		{Name: "电视", MAC: "aa:bb:cc:dd:ee:01", IP: "10.99.0.20", Mode: "direct"},
		{Name: "手机", MAC: "aa:bb:cc:dd:ee:02", IP: "10.99.0.21", Mode: "proxy"},
	}
	for _, mode := range []string{settings.ModeRule, settings.ModeGlobal} {
		s.Mode = mode
		c, _ := build(t, s)
		clash := ModeName(mode)
		v := routeV(t, c, clash, conn{dst: "www.example.com", src: "10.99.0.21", answers: []string{"93.184.216.34", "2606:2800:220:1::1"}})
		if v.out != "proxy" || v.server != "remote" || len(v.resolved) != 1 || !v.resolved[0].Is4() {
			t.Fatalf("%s:强制代理的设备应经远程 DNS 只解析出 IPv4 再走代理,实际 %+v", mode, v)
		}
		for _, src := range []string{"10.99.0.20", "10.99.0.21"} {
			if out, at := route(t, c, clash, conn{dst: "2001:db8::1", src: src}); out != "reject" {
				t.Fatalf("%s:设备 %s 的 IPv6 字面量连接应被拒,实际走了 %s(规则 %d)", mode, src, out, at)
			}
		}
		if mode == settings.ModeRule {
			if out, _ := route(t, c, clash, conn{dst: "93.184.216.34", src: "10.99.0.20"}); out != "direct" {
				t.Fatalf("直连设备的 IPv4 连接照旧直连,实际 %s", out)
			}
		}
	}

	// 开着 IPv6 时不加这一步:节点用不用 IPv6 由它自己定
	s.Mode, s.IPv6 = settings.ModeRule, true
	c, _ := build(t, s)
	if v := routeV(t, c, "Rule", conn{dst: "www.example.com", src: "10.99.0.21"}); v.out != "proxy" || v.server != "" {
		t.Fatalf("开 IPv6 时强制代理的设备不该另外指定解析: %+v", v)
	}
}
