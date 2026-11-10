package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing/service"
)

// 选中线路组时(守护进程给 QuietAuto 的是"别的节点"):选中这一组的成员启动时照测,auto 组和别的线路组都不去碰
// 没选中的节点(它们留着预记的"刚测过",延迟 0);proxy 落在这一组上、组里选中的是它自己的成员;
// 切到另一组时 GroupTest 强制现测那一组。对普通节点调 Group 报"不是选择组"(守护进程靠这个分辨节点和组)。
func TestQuietAutoLineGroup(t *testing.T) {
	// 选中组的成员走 direct 去测本机回环上的这个地址;慢 30 毫秒,测出来的延迟就一定不是预记的 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(30 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	link := srv.URL + "/"

	c := New(nil)
	c.QuietAuto([]string{"a", "b"}) // G2 的成员:这次连接没选它们
	group := func(tag, members string) string {
		return `{"type":"urltest","tag":"` + tag + `","outbounds":[` + members + `],"url":"` + link + `","interval":"24h","idle_timeout":"25h"}`
	}
	cfg := `{"log":{"disabled":true},"outbounds":[
	  {"type":"selector","tag":"proxy","outbounds":["auto","G1","G2","a","b","c","d"],"default":"G1"},
	  ` + group("auto", `"a","b","c","d"`) + `,
	  ` + group("G1", `"c","d"`) + `,
	  ` + group("G2", `"a","b"`) + `,
	  {"type":"socks","tag":"a","server":"127.0.0.1","server_port":1},{"type":"socks","tag":"b","server":"127.0.0.1","server_port":1},
	  {"type":"direct","tag":"c"},{"type":"direct","tag":"d"},
	  {"type":"direct","tag":"direct"}],
	  "experimental":{"cache_file":{"enabled":true,"path":"` + cachePath(t) + `"}}}`
	if err := c.Start([]byte(cfg)); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	_, ctx, _ := c.snapshot()
	h := service.PtrFromContext[urltest.HistoryStorage](ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if x, y := h.LoadURLTestHistory("c"), h.LoadURLTestHistory("d"); x != nil && y != nil && x.Delay > 0 && y.Delay > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, tag := range []string{"c", "d"} {
		if v := h.LoadURLTestHistory(tag); v == nil || v.Delay < 20 {
			t.Fatalf("选中线路组的成员 %s 启动时要真测一次: %+v", tag, v)
		}
	}
	for _, tag := range []string{"a", "b"} {
		if v := h.LoadURLTestHistory(tag); v == nil || v.Delay != 0 {
			t.Fatalf("没选中的节点 %s 启动时不该被 auto / 别的线路组测(应留着预记的历史): %+v", tag, v)
		}
	}
	if now, _, err := c.Group("proxy"); err != nil || now != "G1" {
		t.Fatalf("proxy 应落在选中的线路组上: %q %v", now, err)
	}
	// 读组里的当前项之前,先让 GroupTest 真测一轮(起头的原子标记和启动时那一轮对上):sing-box 的 Now 和它自己换选择之间没有同步
	for i := 0; i < 50; i++ {
		tctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		res, _ := c.GroupTest(tctx, "G1")
		cancel()
		if len(res) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	inner, members, err := c.Group("G1")
	if err != nil || !reflect.DeepEqual(members, []string{"c", "d"}) || (inner != "c" && inner != "d") {
		t.Fatalf("线路组里实际在用的应是它自己的成员: %q %v %v", inner, members, err)
	}

	// 就地切到另一组,强制现测那一组:连不上的成员历史被删掉,不再顶着假的 0 延迟
	if err := c.Select("proxy", "G2"); err != nil {
		t.Fatal(err)
	}
	tctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := c.GroupTest(tctx, "G2"); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"a", "b"} {
		if v := h.LoadURLTestHistory(tag); v != nil {
			t.Fatalf("GroupTest 应强制现测 %s(连不上就删掉历史): %+v", tag, v)
		}
	}
	if _, _, err := c.Group("a"); err == nil || !strings.Contains(err.Error(), "不是选择组") {
		t.Fatalf("对普通节点调 Group 应报不是选择组: %v", err)
	}
}
