package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
