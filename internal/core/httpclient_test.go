package core

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// cachePath 测试内核的缓存文件放临时目录:不写的话 sing-box 会落到进程当前目录的 cache.db(见 Probe 的说明)。
func cachePath(t *testing.T) string {
	t.Helper()
	return filepath.ToSlash(filepath.Join(t.TempDir(), "cache.db"))
}

// HTTPClient 用完不能留空闲连接:严格全局下拉订阅最后一跳是 direct,留下的连接挂在隧道外发 keepalive。
// 只起出站(没有入站、不碰路由),请求打本机回环。
func TestHTTPClientLeavesNoIdleConnection(t *testing.T) {
	var mu sync.Mutex
	states := map[net.Conn]http.ConnState{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	srv.Config.ConnState = func(c net.Conn, s http.ConnState) {
		mu.Lock()
		states[c] = s
		mu.Unlock()
	}
	srv.Start()
	defer srv.Close()

	c := New(nil)
	cfg := `{"log":{"level":"error"},"outbounds":[{"type":"direct","tag":"direct"}],
"experimental":{"cache_file":{"enabled":true,"path":"` + cachePath(t) + `"}}}`
	if err := c.Start([]byte(cfg)); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	cl, err := c.HTTPClient("direct", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cl.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.TrimSpace(string(b)) != "ok" {
		t.Fatalf("响应不对: %q", b)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		open := 0
		for _, s := range states {
			if s != http.StateClosed && s != http.StateHijacked {
				open++
			}
		}
		n := len(states)
		mu.Unlock()
		if n > 0 && open == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("请求做完 2 秒了,服务端还有 %d 条连接没关(空闲连接留在池里)", open)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
