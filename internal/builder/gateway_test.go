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
