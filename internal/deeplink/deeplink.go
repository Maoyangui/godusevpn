// Package deeplink 落地页「一键导入」的 godusevpn:// 链接:解析,以及(Linux 的浏览器面板)换成面板地址。
//
//	godusevpn://import?url=<订阅地址>[&name=<订阅名>]
//
// Windows / macOS 由图形界面接住链接、弹出填好的添加订阅框;Android 由宿主接住。
// Linux 没有图形界面,界面是守护进程自带的网页面板:系统把链接交给 `godusevpn open-link`,它用默认浏览器打开面板,
// 要导入的地址放在 # 后面(片段不发给服务器,进不了任何日志),页面读到后走同一个添加订阅框,由用户点确认。
package deeplink

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// Scheme 协议名。
const Scheme = "godusevpn"

// Import 一次导入:订阅地址与(可选的)订阅名。
type Import struct {
	URL  string
	Name string
}

var (
	ErrNotLink = errors.New("不是 godusevpn:// 链接")
	ErrNoURL   = errors.New("链接里没有订阅地址")
	ErrBadURL  = errors.New("订阅地址要以 http:// 或 https:// 开头")
)

// ParseImport 解析一条一键导入链接。只认 import 这一个动作;订阅地址必须是 http / https
// (链接是网页给的,别的协议一律不接:file://、javascript: 之类不该从这里进来)。
func ParseImport(link string) (Import, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || !strings.EqualFold(u.Scheme, Scheme) || !strings.EqualFold(u.Host, "import") {
		return Import{}, ErrNotLink
	}
	q := u.Query()
	sub := strings.TrimSpace(q.Get("url"))
	if sub == "" {
		return Import{}, ErrNoURL
	}
	su, err := url.Parse(sub)
	if err != nil || su.Host == "" || (su.Scheme != "http" && su.Scheme != "https") {
		return Import{}, ErrBadURL
	}
	name := strings.TrimSpace(q.Get("name"))
	if r := []rune(name); len(r) > 80 { // 名字只是给人看的,太长的截掉
		name = string(r[:80])
	}
	return Import{URL: sub, Name: name}, nil
}

// Fragment 放在面板地址后面的片段(带 #)。页面里 takeHashImport 读的就是这个写法,两边要一起改。
func (i Import) Fragment() string {
	v := url.Values{}
	v.Set("url", i.URL)
	if i.Name != "" {
		v.Set("name", i.Name)
	}
	return "#import?" + v.Encode()
}

// LocalURL 把面板的监听地址换成本机浏览器能打开的地址:监听全部地址或回环时用 127.0.0.1,
// 只监听某个地址时就用那个地址。listen 为空(没开面板)或写法不对返回空。
func LocalURL(listen string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil || port == "" {
		return ""
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if host == "" || (ip != nil && (ip.IsUnspecified() || ip.IsLoopback())) || strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}
