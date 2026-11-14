package profile

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `{
  "log": {"level": "info"},
  "dns": {"servers": [{"type": "local", "tag": "local"}]},
  "inbounds": [{"type": "tun", "tag": "tun-in"}],
  "outbounds": [
    {"type": "selector", "tag": "proxy", "outbounds": ["auto", "香港1"]},
    {"type": "urltest", "tag": "auto", "outbounds": ["香港1"]},
    {"type": "hysteria2", "tag": "香港1", "server": "1.2.3.4", "server_port": 443, "password": "p"},
    {"type": "anytls", "tag": "proxy", "server": "1.2.3.5", "server_port": 8443, "password": "p"},
    {"type": "vless", "server": "1.2.3.6", "server_port": 443, "uuid": "u"},
    {"type": "direct", "tag": "direct"}
  ],
  "route": {"final": "proxy"}
}`

func TestParseKeepsOnlyNodes(t *testing.T) {
	h := http.Header{}
	h.Set("Profile-Title", "base64:5rWL6K+V") // 测试
	h.Set("Subscription-Userinfo", "upload=10; download=20; total=100; expire=1760000000")
	p, err := Parse([]byte(sample), h)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Outbounds) != 3 {
		t.Fatalf("应只留 3 个节点,实际 %d: %v", len(p.Outbounds), p.Tags)
	}
	// 和保留名撞的 tag 要改名,没 tag 的按类型起名
	if p.Tags[0] != "香港1" || p.Tags[1] != "proxy 2" || p.Tags[2] != "vless" {
		t.Fatalf("tag 清洗不对: %v", p.Tags)
	}
	if p.Title != "测试" {
		t.Fatalf("标题应解 base64: %q", p.Title)
	}
	if p.Usage.Download != 20 || p.Usage.Total != 100 || p.Usage.Expire != 1760000000 {
		t.Fatalf("用量头解析不对: %+v", p.Usage)
	}
	if got := p.Servers(); len(got) != 3 || got[0] != "1.2.3.4:443" {
		t.Fatalf("服务器列表: %v", got)
	}
}

func TestParseRejectsEmpty(t *testing.T) {
	if _, err := Parse([]byte(`{"outbounds":[{"type":"direct","tag":"direct"}]}`), nil); err != ErrNoNodes {
		t.Fatalf("空订阅应报 ErrNoNodes: %v", err)
	}
	if _, err := Parse([]byte(`not json`), nil); err == nil {
		t.Fatal("非 JSON 应报错")
	}
}

func TestFetchAddsFormatAndHandlesStatus(t *testing.T) {
	var gotUA, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotQuery = r.UserAgent(), r.URL.RawQuery
		switch r.URL.Path {
		case "/sub/alice":
			w.Header().Set("Profile-Title", "utf-8''%E5%86%92%E5%A4%AE")
			w.Write([]byte(sample))
		case "/sub/gone":
			w.WriteHeader(404)
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	p, err := Fetch(context.Background(), srv.URL+"/sub/alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "format=json" {
		t.Fatalf("应自动补 format=json: %q", gotQuery)
	}
	if gotUA == "" || gotUA[:10] != "godusevpn/" {
		t.Fatalf("UA 应标明客户端: %q", gotUA)
	}
	if p.Title != "冒央" || p.URL != srv.URL+"/sub/alice" {
		t.Fatalf("标题或地址不对: %q %q", p.Title, p.URL)
	}
	_, err = Fetch(context.Background(), srv.URL+"/sub/gone", nil)
	var fe *FetchError
	if err == nil || !asFetch(err, &fe) || fe.Status != 404 {
		t.Fatalf("404 应带状态码: %v", err)
	}
	if _, err := Fetch(context.Background(), srv.URL+"/sub/broken", nil); err == nil {
		t.Fatal("500 应报错")
	}
	if _, err := Fetch(context.Background(), "ftp://x", nil); err == nil {
		t.Fatal("非 http 地址应报错")
	}
	// 缓存读写
	path := filepath.Join(t.TempDir(), "profile.json")
	if err := p.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil || len(back.Outbounds) != len(p.Outbounds) || back.Title != p.Title {
		t.Fatalf("缓存读回不一致: %v", err)
	}
}

// 网络失败(超时、拒绝连接、TLS 失败)时 net/http 给的 *url.Error 带着完整请求地址,路径就是订阅令牌。
// 这段文本会进界面、服务日志与诊断信息:只能留主机,看得出是在哪台面板上失败的。
func TestFetchNetErrorHidesToken(t *testing.T) {
	cl := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	_, err := Fetch(context.Background(), "https://panel.example:2056/sub/SECRETTOKEN123", cl)
	var fe *FetchError
	if !asFetch(err, &fe) {
		t.Fatalf("应返回 FetchError: %v", err)
	}
	if strings.Contains(fe.Msg, "SECRETTOKEN123") || strings.Contains(fe.Msg, "/sub/") || strings.Contains(fe.Msg, "format=json") {
		t.Fatalf("错误文本带着订阅路径: %s", fe.Msg)
	}
	if !strings.Contains(fe.Msg, "https://panel.example:2056/***") || !strings.Contains(fe.Msg, "connection refused") {
		t.Fatalf("主机或失败原因丢了: %s", fe.Msg)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func asFetch(err error, target **FetchError) bool {
	fe, ok := err.(*FetchError)
	if ok {
		*target = fe
	}
	return ok
}

// 用户从 m-ui 落地页复制了 Clash 地址(?format=clash)或浏览器地址(?format=page)粘进来,也要拿 sing-box JSON:
// 客户端只认这一种;已经是 JSON 一族的写法(不分大小写)原样保留,其它参数不动。
func TestFetchForcesJSONFormat(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(sample))
	}))
	defer srv.Close()
	cases := map[string]string{
		"?format=clash":     "format=json",
		"?format=page":      "format=json",
		"?format=Clash&x=1": "format=json&x=1",
		"?format=sing-box":  "format=sing-box",
		"?format=JSON":      "format=JSON",
		"?format=sfi":       "format=sfi",
		"?token=abc":        "format=json&token=abc",
	}
	for suffix, want := range cases {
		if _, err := Fetch(context.Background(), srv.URL+"/sub/alice"+suffix, nil); err != nil {
			t.Fatalf("%s: %v", suffix, err)
		}
		if gotQuery != want {
			t.Errorf("%s:请求参数应为 %q,得 %q", suffix, want, gotQuery)
		}
	}
}

// 为避免重名给节点改名时,别的节点 detour 里写的旧名要跟着改 —— 只改真正指向被改名节点的那些:
// 订阅里只有这一个出站叫这个名字(sing-box 的 tag 本来唯一);同名的有好几个时说不清指的是谁,留着(留下的就是第一个,它没改名)。
func TestParseRewritesDetourOnRename(t *testing.T) {
	body := `{"outbounds": [
	  {"type": "shadowsocks", "tag": "a", "server": "1.1.1.1", "server_port": 1},
	  {"type": "shadowsocks", "tag": "a", "server": "1.1.1.2", "server_port": 1},
	  {"type": "shadowsocks", "tag": "a 2", "server": "1.1.1.3", "server_port": 1},
	  {"type": "shadowsocks", "tag": " hop ", "server": "1.1.1.4", "server_port": 1},
	  {"type": "socks", "tag": "auto", "server": "1.1.1.5", "server_port": 1},
	  {"type": "socks", "tag": "direct", "server": "1.1.1.6", "server_port": 1},
	  {"type": "direct", "tag": "direct"},
	  {"type": "vless", "tag": "via-a", "server": "2.2.2.1", "server_port": 1, "detour": "a"},
	  {"type": "vless", "tag": "via-a2", "server": "2.2.2.2", "server_port": 1, "detour": "a 2"},
	  {"type": "vless", "tag": "via-hop", "server": "2.2.2.3", "server_port": 1, "detour": " hop "},
	  {"type": "vless", "tag": "via-auto", "server": "2.2.2.4", "server_port": 1, "detour": "auto"},
	  {"type": "vless", "tag": "via-direct", "server": "2.2.2.5", "server_port": 1, "detour": "direct"},
	  {"type": "vless", "tag": "via-none", "server": "2.2.2.6", "server_port": 1}
	]}`
	p, err := Parse([]byte(body), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Tags[:6], "|"); got != "a|a 2|a 2 2|hop|auto 2|direct 2" {
		t.Fatalf("改名: %s", got)
	}
	detour := map[string]string{}
	raw := map[string]string{}
	for i, r := range p.Outbounds {
		var n struct {
			Detour string `json:"detour"`
		}
		if err := json.Unmarshal(r, &n); err != nil {
			t.Fatal(err)
		}
		detour[p.Tags[i]] = n.Detour
		raw[p.Tags[i]] = string(r)
	}
	for tag, want := range map[string]string{
		"via-a":      "a",      // 订阅里两个都叫 a:说不清,留着(指留下名字的第一个)
		"via-a2":     "a 2 2",  // 原来叫 a 2 的那台被挤成了 a 2 2;不改就指到了重名改出来的那台
		"via-hop":    "hop",    // 去掉首尾空白
		"via-auto":   "auto 2", // 和客户端保留名撞了
		"via-direct": "direct", // 订阅里还有个 direct 类型的出站也叫 direct:说不清,留着
		"via-none":   "",
	} {
		if detour[tag] != want {
			t.Errorf("%s 的 detour = %q,要 %q", tag, detour[tag], want)
		}
	}
	// 没改的不动字节
	if !strings.Contains(raw["via-a"], `"detour": "a"`) {
		t.Errorf("detour 没改的节点字节变了:%s", raw["via-a"])
	}
}
