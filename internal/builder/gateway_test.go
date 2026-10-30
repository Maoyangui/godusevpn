package builder

import (
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 网关模式:TUN 开 auto_redirect;设备策略按 MAC 渲染成规则,放在模式分支之前;本机模式什么都不加。
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
		src, _ := r["source_mac_address"].([]any)
		switch {
		case len(src) > 0 && r["outbound"] == "direct":
			iDirect = i
			if len(src) != 2 || src[0] != "aa:bb:cc:dd:ee:01" || src[1] != "aa:bb:cc:dd:ee:05" {
				t.Fatalf("直连设备不对(不在线的设备也要算): %v", src)
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
	if strings.Contains(raw, "aa:bb:cc:dd:ee:04") {
		t.Fatal("跟随规则的设备不该出现")
	}
	if strings.Contains(raw, "source_ip_cidr") {
		t.Fatal("有 MAC 的设备不该再按 IP 匹配:IP 会被分给别的设备")
	}

	s.NetMode = settings.NetLocal
	_, raw = build(t, s)
	if strings.Contains(raw, "auto_redirect") || strings.Contains(raw, "source_ip_cidr") || strings.Contains(raw, "source_mac_address") {
		t.Fatal("本机模式不该有网关相关配置")
	}
}

// 这次要修的毛病:策略按 IP 生效时,设备离线后它的 IP 分给别人,别人也被套用;设备换了 IP 要等一轮扫描、再重启内核。
// 现在只认 MAC:同一个 IP 换了 MAC 就不算,同一台设备换任何地址(含 IPv6)都算。
func TestGatewayPolicyFollowsMAC(t *testing.T) {
	s := settings.Default()
	s.IPv6 = true
	s.NetMode = settings.NetGateway
	s.Devices = []settings.Device{{Name: "小孩平板", MAC: "aa:bb:cc:dd:ee:03", IP: "10.99.0.22", Mode: "reject"}}
	c, _ := build(t, s)
	for _, tc := range []struct {
		name string
		cn   conn
		want bool
	}{
		{"平板还在原来的 IP", conn{dst: "93.184.216.34", src: "10.99.0.22", mac: "aa:bb:cc:dd:ee:03"}, true},
		{"平板换了 IP", conn{dst: "93.184.216.34", src: "10.99.0.77", mac: "aa:bb:cc:dd:ee:03"}, true},
		{"平板的 IPv6 地址", conn{dst: "2001:db8::1", src: "2001:db8:1::5", mac: "aa:bb:cc:dd:ee:03"}, true},
		{"旧 IP 分给了别的设备", conn{dst: "93.184.216.34", src: "10.99.0.22", mac: "aa:bb:cc:dd:ee:99"}, false},
	} {
		out, at := route(t, c, "Rule", tc.cn)
		if (out == "reject") != tc.want {
			t.Errorf("%s:走了 %s(规则 %d),应%s拒绝", tc.name, out, at, map[bool]string{true: "", false: "不"}[tc.want])
		}
	}
}

// 只记了 IP 没有 MAC 的老设备照旧按来源地址;一条设备策略不能把 MAC 和 IP 写进同一条规则(那是"且",谁都匹配不上)。
func TestGatewayLegacyIPDevice(t *testing.T) {
	s := settings.Default()
	s.NetMode = settings.NetGateway
	s.Devices = []settings.Device{
		{Name: "老设备", IP: "10.99.0.30", Mode: "reject"},
		{Name: "新设备", MAC: "aa:bb:cc:dd:ee:03", Mode: "reject"},
	}
	c, _ := build(t, s)
	for _, r := range c.Route.Rules {
		if r["source_mac_address"] != nil && r["source_ip_cidr"] != nil {
			t.Fatalf("MAC 和 IP 写进了同一条规则: %v", r)
		}
	}
	if out, _ := route(t, c, "Rule", conn{dst: "93.184.216.34", src: "10.99.0.30"}); out != "reject" {
		t.Fatalf("只记了 IP 的设备应按来源地址拒绝,实际 %s", out)
	}
	if out, _ := route(t, c, "Rule", conn{dst: "93.184.216.34", src: "10.99.0.31", mac: "aa:bb:cc:dd:ee:03"}); out != "reject" {
		t.Fatalf("记了 MAC 的设备应按 MAC 拒绝,实际 %s", out)
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
		v := routeV(t, c, clash, conn{dst: "www.example.com", src: "10.99.0.21", mac: "aa:bb:cc:dd:ee:02", answers: []string{"93.184.216.34", "2606:2800:220:1::1"}})
		if v.out != "proxy" || v.server != "remote" || len(v.resolved) != 1 || !v.resolved[0].Is4() {
			t.Fatalf("%s:强制代理的设备应经远程 DNS 只解析出 IPv4 再走代理,实际 %+v", mode, v)
		}
		for src, mac := range map[string]string{"10.99.0.20": "aa:bb:cc:dd:ee:01", "10.99.0.21": "aa:bb:cc:dd:ee:02"} {
			if out, at := route(t, c, clash, conn{dst: "2001:db8::1", src: src, mac: mac}); out != "reject" {
				t.Fatalf("%s:设备 %s 的 IPv6 字面量连接应被拒,实际走了 %s(规则 %d)", mode, src, out, at)
			}
		}
		if mode == settings.ModeRule {
			if out, _ := route(t, c, clash, conn{dst: "93.184.216.34", src: "10.99.0.20", mac: "aa:bb:cc:dd:ee:01"}); out != "direct" {
				t.Fatalf("直连设备的 IPv4 连接照旧直连,实际 %s", out)
			}
		}
	}

	// 开着 IPv6 时不加这一步:节点用不用 IPv6 由它自己定
	s.Mode, s.IPv6 = settings.ModeRule, true
	c, _ := build(t, s)
	if v := routeV(t, c, "Rule", conn{dst: "www.example.com", src: "10.99.0.21", mac: "aa:bb:cc:dd:ee:02"}); v.out != "proxy" || v.server != "" {
		t.Fatalf("开 IPv6 时强制代理的设备不该另外指定解析: %+v", v)
	}
}
