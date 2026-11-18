package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/uiapi"
)

// fakeDaemon 只回设置;记下有没有人真的调到了守护进程的方法。
type fakeDaemon struct {
	s      settings.Settings
	called []string
}

func (f *fakeDaemon) Dispatch(method string, _ json.RawMessage) (any, error) {
	if method != ipc.MGetSettings {
		f.called = append(f.called, method)
	}
	return f.s, nil
}
func (f *fakeDaemon) Logf(string, ...any) {}

func post(t *testing.T, h http.Handler, path, body, remote, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.RemoteAddr = remote
	req.Host = "192.168.1.2:9800" // httptest 默认的 Host 是 example.com,会被 Host 检查挡下(见 allowedHost)
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// 没设密码时面板除了 ping 什么都不给,本机来的也一样:以前回环一律放行,同机任何进程都能撤闸、改设置、读订阅地址,
// 绕过了只给 root / admin 组的控制口。
func TestNoPasswordLocksPanelEvenForLoopback(t *testing.T) {
	f := &fakeDaemon{s: settings.Default()}
	f.s.WebListen, f.s.WebPassword = "127.0.0.1:9800", ""
	h := New(uiapi.New(f, uiapi.Options{}), nil).Handler()
	for _, remote := range []string{"127.0.0.1:50000", "[::1]:50000", "192.168.1.9:50000"} {
		for _, path := range []string{"/api/Disconnect", "/api/SaveSettings", "/api/GetSettings", "/api/login"} {
			rec := post(t, h, path, `{"password":"anything"}`, remote, "")
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "NEED_PASSWORD") {
				t.Fatalf("%s 从 %s:没设密码应当 403 NEED_PASSWORD,得到 %d %s", path, remote, rec.Code, rec.Body.String())
			}
		}
	}
	if len(f.called) != 0 {
		t.Fatalf("没设密码时不该调到守护进程:%v", f.called)
	}
	if rec := post(t, h, "/api/ping", "", "127.0.0.1:1", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"auth":false`) {
		t.Fatalf("ping 要照常回,页面靠它决定显示什么:%d %s", rec.Code, rec.Body.String())
	}
}

// 设了密码:和原来一样要登录,登录后能用;回环不再免登录。
func TestPasswordLoginStillWorks(t *testing.T) {
	f := &fakeDaemon{s: settings.Default()}
	if err := f.s.SetWebPassword("secret123"); err != nil {
		t.Fatal(err)
	}
	h := New(uiapi.New(f, uiapi.Options{}), nil).Handler()
	if rec := post(t, h, "/api/GetSettings", "[]", "127.0.0.1:1", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("没登录应当 401,得到 %d", rec.Code)
	}
	if rec := post(t, h, "/api/login", `{"password":"wrong"}`, "127.0.0.1:1", ""); strings.Contains(rec.Header().Get("Set-Cookie"), "gvsid=") {
		t.Fatal("密码不对不该发会话")
	}
	rec := post(t, h, "/api/login", `{"password":"secret123"}`, "192.168.1.9:1", "")
	c := rec.Result().Cookies()
	if len(c) == 0 || c[0].Name != "gvsid" {
		t.Fatalf("登录没拿到会话:%d %s", rec.Code, rec.Body.String())
	}
	if rec := post(t, h, "/api/GetSettings", "[]", "192.168.1.9:1", "gvsid="+c[0].Value); rec.Code != 200 || strings.Contains(rec.Body.String(), `"error"`) {
		t.Fatalf("登录后应当能用:%d %s", rec.Code, rec.Body.String())
	}
}

// 审计 S2:只认本机的名字,挡 DNS 重绑定。IP、localhost、不带点的主机名、局域网后缀直接放行;别的域名要写进 webHosts。
func TestAllowedHost(t *testing.T) {
	for _, c := range []struct {
		host, extra string
		want        bool
	}{
		{"127.0.0.1:9800", "", true}, {"192.168.1.2:9800", "", true}, {"[::1]:9800", "", true}, {"[fe80::1%25eth0]:9800", "", true},
		{"100.64.1.1", "", true}, {"localhost:9800", "", true}, {"LOCALHOST", "", true}, {"app.localhost:9800", "", true},
		{"nanopi:9800", "", true}, {"router.lan:9800", "", true}, {"pi.local", "", true}, {"box.home.arpa", "", true},
		{"nas.internal", "", true}, {"x.localdomain", "", true}, {"box.tail1234.ts.net", "", true}, {"", "", true},
		{"evil.example.com:9800", "", false}, {"127.0.0.1.nip.io:9800", "", false}, {"localhost.evil.com", "", false},
		{"lan.evil.com", "", false}, {"evil.com.", "", false},
		{"panel.example.com", "panel.example.com", true}, {"Panel.Example.com.:443", " panel.example.com ", true},
		{"a.panel.example.com", "*.panel.example.com", true}, {"panel.example.com", "*.panel.example.com", false},
		{"evilpanel.example.com", "*.panel.example.com", false}, {"other.example.com", "a.com,other.example.com", true},
		{"evil.example.com", "*", true}, {"evil.example.com", "panel.example.com", false},
	} {
		if got := allowedHost(c.host, c.extra); got != c.want {
			t.Fatalf("Host %q(webHosts=%q):得 %v,应为 %v", c.host, c.extra, got, c.want)
		}
	}
}

// 借重绑定打进来的请求(Host 是攻击者的域名)连 ping 和登录都碰不到,也不记登录失败(用户自己不会被锁在外面);
// 加进 webHosts 或开了 webAllowPublic 就照常。
func TestHostCheckBlocksRebinding(t *testing.T) {
	req := func(h http.Handler, path, host string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"password":"wrong"}`))
		r.RemoteAddr, r.Host = "127.0.0.1:50000", host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	f := &fakeDaemon{s: settings.Default()}
	if err := f.s.SetWebPassword("secret123"); err != nil {
		t.Fatal(err)
	}
	s := New(uiapi.New(f, uiapi.Options{}), nil)
	h := s.Handler()
	for _, path := range []string{"/api/ping", "/api/login", "/", "/api/GetSettings"} {
		rec := req(h, path, "evil.example.com:9800")
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "settings webHosts=evil.example.com\n") {
			t.Fatalf("%s:陌生域名应 403 并写明怎么放行,得 %d %s", path, rec.Code, rec.Body.String())
		}
	}
	if len(s.fails) != 0 || len(f.called) != 0 {
		t.Fatalf("被挡下的请求不该记登录失败、不该调到守护进程: %v %v", s.fails, f.called)
	}
	if rec := req(h, "/api/ping", "127.0.0.1:9800"); rec.Code != 200 {
		t.Fatalf("IP 直连应照常: %d", rec.Code)
	}

	f2 := &fakeDaemon{s: f.s}
	f2.s.WebHosts = "evil.example.com"
	if rec := req(New(uiapi.New(f2, uiapi.Options{}), nil).Handler(), "/api/ping", "evil.example.com:9800"); rec.Code != 200 {
		t.Fatalf("写进 webHosts 的域名应放行: %d %s", rec.Code, rec.Body.String())
	}
	f3 := &fakeDaemon{s: f.s}
	f3.s.WebAllowPublic = true
	if rec := req(New(uiapi.New(f3, uiapi.Options{}), nil).Handler(), "/api/ping", "evil.example.com:9800"); rec.Code != 200 {
		t.Fatalf("开了 webAllowPublic 的不查 Host: %d %s", rec.Code, rec.Body.String())
	}
}

// 面板默认只给本机和局域网;回应里不写产品名。
func TestLocalClient(t *testing.T) {
	for host, want := range map[string]bool{"127.0.0.1": true, "::1": true, "192.168.1.9": true, "10.1.2.3": true, "172.16.0.1": true, "100.64.1.1": true, "fe80::1": true, "fd00::1": true,
		"8.8.8.8": false, "20.44.159.84": false, "2001:4860:4860::8888": false, "not-an-ip": false} {
		if got := localClient(host); got != want {
			t.Fatalf("%s: 得 %v", host, got)
		}
	}
}

// 面板开始监听时报一次监听地址、停下来时报一次空:Linux 的守护进程靠它记下「面板开在哪」(桌面上点 godusevpn:// 链接用)。
func TestAnnounceListenAndStop(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	f := &fakeDaemon{s: settings.Default()}
	f.s.WebListen, f.s.WebPassword = addr, "x"
	s := New(uiapi.New(f, uiapi.Options{}), nil)
	got := make(chan string, 4)
	s.Announce = func(listen string) { got <- listen }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.serveOnce(ctx, addr) }()
	select {
	case v := <-got:
		if v != addr {
			t.Fatalf("开始监听时应当报监听地址 %q,得到 %q", addr, v)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("没等到开始监听的通知")
	}
	if c, err := net.DialTimeout("tcp", addr, 2*time.Second); err != nil {
		t.Fatalf("报了地址就应当已经在监听: %v", err)
	} else {
		c.Close()
	}
	cancel()
	select {
	case v := <-got:
		if v != "" {
			t.Fatalf("停下来时应当报空,得到 %q", v)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("没等到停止监听的通知")
	}
	if err := <-done; err != nil {
		t.Fatalf("正常停下不该报错: %v", err)
	}
}
