package profile

import (
	"net/http"
	"strings"
	"testing"
)

// 面板给的「选购 / 续费」地址随 Profile-Web-Page-Url 头下来,客户端存进 Profile.WebPage,
// 订阅卡片上那颗续费按钮就是拿它开的。地址来自订阅响应,属于外部输入,只认 http(s)。
func TestWebPageHeader(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"主面板的", "https://liumeiti.vip/services/airport-node", "https://liumeiti.vip/services/airport-node"},
		{"代理自己的", "https://test.com", "https://test.com"},
		{"两边空白", "  https://x.example/buy  ", "https://x.example/buy"},
		{"http 也行", "http://x.example/buy", "http://x.example/buy"},
		{"大小写不敏感", "HTTPS://X.example/buy", "HTTPS://X.example/buy"},
		{"没配", "", ""},
		{"javascript", "javascript:alert(1)", ""},
		{"本地文件", "file:///c:/windows/system32", ""},
		{"应用协议", "godusevpn://import?url=x", ""},
		{"没有协议", "liumeiti.vip/buy", ""},
		{"夹了换行", "https://x.example/buy\nnope", ""},
		{"夹了制表符", "https://x.example/\tbuy", ""},
		{"超长", "https://x.example/" + strings.Repeat("a", 3000), ""},
	} {
		h := http.Header{}
		if c.in != "" {
			h.Set("Profile-Web-Page-Url", c.in)
		}
		p, err := Parse([]byte(sample), h)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if p.WebPage != c.want {
			t.Fatalf("%s:应得 %q,实际 %q", c.name, c.want, p.WebPage)
		}
	}
	// 面板没发这个头(老版本)也不能出错,只是没有续费入口
	p, err := Parse([]byte(sample), http.Header{})
	if err != nil || p.WebPage != "" {
		t.Fatalf("老面板不发这个头时应为空:%q %v", p.WebPage, err)
	}
}
