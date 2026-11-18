package settings

import "testing"

// 审计 S2:面板还认的域名(webHosts)只收域名(可带 *. 前缀)或单独一个 *;规范成小写、去空格、去末尾的点。
// 命令行 settings webHosts=… 直接能改(Apply 走的就是这条校验)。
func TestWebHostsValidate(t *testing.T) {
	s := Default()
	out, err := s.Apply("webHosts", " Panel.Example.com. , *.lan.example.org ,")
	if err != nil {
		t.Fatal(err)
	}
	if out.WebHosts != "panel.example.com,*.lan.example.org" {
		t.Fatalf("应规范成小写、去空格、去末尾的点: %q", out.WebHosts)
	}
	for _, ok := range []string{"*", "", "nas_1.example.com"} {
		if out, err := s.Apply("webHosts", ok); err != nil || out.WebHosts != ok {
			t.Fatalf("%q 应能存: %v %q", ok, err, out.WebHosts)
		}
	}
	for _, bad := range []string{"http://panel.example.com", "panel.example.com:443", "panel.example.com/x", "pa nel.com", "*.", "a..b", "-a.com"} {
		if _, err := s.Apply("webHosts", bad); err == nil {
			t.Fatalf("%q 应被拒", bad)
		}
	}
	if d := Default(); d.WebHosts != "" {
		t.Fatalf("出厂值应为空(只认 IP、localhost 和局域网主机名): %q", d.WebHosts)
	}
}
