package deeplink

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestParseImport(t *testing.T) {
	sub := "https://sub.example.com:2096/sub/abcDEF123?format=singbox"
	link := "godusevpn://import?url=" + url.QueryEscape(sub) + "&name=" + url.QueryEscape("我的 订阅")
	got, err := ParseImport(link)
	if err != nil || got.URL != sub || got.Name != "我的 订阅" {
		t.Fatalf("解析不对: %+v %v", got, err)
	}
	// 协议名、动作名不分大小写;名字可以没有;前后空白不算
	if got, err := ParseImport("  GODUSEVPN://Import?url=" + url.QueryEscape("http://10.0.0.1/sub/x") + "\n"); err != nil || got.URL != "http://10.0.0.1/sub/x" || got.Name != "" {
		t.Fatalf("大小写 / 空白: %+v %v", got, err)
	}
	for link, want := range map[string]error{
		"":                        ErrNotLink,
		"https://example.com/":    ErrNotLink,
		"godusevpn://connect":     ErrNotLink, // 只认 import
		"godusevpn://import":      ErrNoURL,
		"godusevpn://import?url=": ErrNoURL,
		"godusevpn://import?url=file%3A%2F%2F%2Fetc%2Fpasswd": ErrBadURL,
		"godusevpn://import?url=javascript%3Aalert(1)":        ErrBadURL,
		"godusevpn://import?url=sub.example.com%2Fsub%2Fx":    ErrBadURL, // 没写协议
		"godusevpn://import?url=https%3A%2F%2F":               ErrBadURL, // 没有主机
	} {
		if _, err := ParseImport(link); !errors.Is(err, want) {
			t.Errorf("%q 应当报 %v,实际 %v", link, want, err)
		}
	}
	long, _ := ParseImport("godusevpn://import?url=" + url.QueryEscape(sub) + "&name=" + url.QueryEscape(strings.Repeat("名", 200)))
	if n := len([]rune(long.Name)); n != 80 {
		t.Fatalf("太长的名字截到 80 个字,实际 %d", n)
	}
}

// 片段要原样带得回去:页面用 URLSearchParams 读,和这里的编码是同一套(空格是 +,& = # 都转义)。
func TestFragmentRoundTrip(t *testing.T) {
	in := Import{URL: "https://sub.example.com/sub/a b?x=1&y=2#frag", Name: "A&B = 测试 #1"}
	f := in.Fragment()
	if !strings.HasPrefix(f, "#import?") || strings.ContainsAny(f[len("#import?"):], " #") {
		t.Fatalf("片段写法不对: %q", f)
	}
	v, err := url.ParseQuery(f[len("#import?"):])
	if err != nil || v.Get("url") != in.URL || v.Get("name") != in.Name {
		t.Fatalf("读回来不一样: %v %v", v, err)
	}
	if f := (Import{URL: "https://a.example/sub/x"}).Fragment(); strings.Contains(f, "name=") {
		t.Fatalf("没有名字时不带 name: %q", f)
	}
}

func TestLocalURL(t *testing.T) {
	for listen, want := range map[string]string{
		"0.0.0.0:9800":     "http://127.0.0.1:9800/",
		":9800":            "http://127.0.0.1:9800/",
		"[::]:9800":        "http://127.0.0.1:9800/",
		"127.0.0.1:9801":   "http://127.0.0.1:9801/",
		"[::1]:9800":       "http://127.0.0.1:9800/",
		"localhost:9800":   "http://127.0.0.1:9800/",
		"192.168.1.1:9800": "http://192.168.1.1:9800/",
		"[fd00::1]:9800":   "http://[fd00::1]:9800/",
		" 0.0.0.0:9800 ":   "http://127.0.0.1:9800/",
		"":                 "",
		"9800":             "",
		"0.0.0.0":          "",
	} {
		if got := LocalURL(listen); got != want {
			t.Errorf("LocalURL(%q) = %q,要 %q", listen, got, want)
		}
	}
}

// 页面读片段的写法(web/dist/app.js 的 takeHashImport)和这里的 Fragment 是一对:前缀、前缀长度改了一边,另一边要跟着改。
func TestFragmentMatchesPage(t *testing.T) {
	b, err := os.ReadFile("../../web/dist/app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	const prefix = "#import?"
	if !strings.HasPrefix((Import{URL: "https://a.example/sub/x"}).Fragment(), prefix) {
		t.Fatal("Fragment 的前缀变了")
	}
	for _, want := range []string{
		"h.indexOf('" + prefix + "') !== 0",
		fmt.Sprintf("new URLSearchParams(h.slice(%d))", len(prefix)),
		"p.get('url')", "p.get('name')",
		"history.replaceState(null, '', location.pathname + location.search)", // 读完要从地址栏拿掉:地址带着令牌
		"window.__webEmit('import', imp)",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("app.js 里找不到 %q:页面读不到一键导入的片段了", want)
		}
	}
	api, err := os.ReadFile("../../web/dist/api.js")
	if err != nil || !strings.Contains(string(api), "window.__webEmit = ") {
		t.Fatalf("api.js 里要有 __webEmit(浏览器面板自己发事件用): %v", err)
	}
}
