package profile

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func asFetch(err error, target **FetchError) bool {
	fe, ok := err.(*FetchError)
	if ok {
		*target = fe
	}
	return ok
}
