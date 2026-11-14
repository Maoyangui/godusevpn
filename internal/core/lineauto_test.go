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

// GroupBusy 认得出测速组"正在测":sing-box 碰上正在测的一轮会立刻回一个空结果,和"真测完、全不通"长得一模一样,
// 守护进程的组内自救要靠这个分清"还在测"和"整组不通"(不然撞上定时测速那一轮就会误判、跨线路借道)。
// 读的是 sing-box 内部的标记:换了 sing-box 版本这条测试会先红。
func TestGroupBusy(t *testing.T) {
	release := make(chan struct{})
	arrived := make(chan struct{}, 4) // 这一轮的测速请求真到了
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		arrived <- struct{}{}
		<-release                         // 测速请求一直挂着,这一轮就一直"在测"
		time.Sleep(20 * time.Millisecond) // 放行之后再慢一点:测出来的延迟不会是 0(0 是"没测出来")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	defer close(release)
	c := New(nil)
	if busy, known := c.GroupBusy("G"); busy || known {
		t.Fatal("内核没跑时说不清")
	}
	c.QuietAuto([]string{"m"}) // 启动那一轮跳过它:这一轮什么时候开始由测试来定
	cfg := `{"log":{"disabled":true},"outbounds":[
	  {"type":"selector","tag":"proxy","outbounds":["G","m"],"default":"G"},
	  {"type":"urltest","tag":"G","outbounds":["m"],"url":"` + srv.URL + `/","interval":"24h","idle_timeout":"25h"},
	  {"type":"direct","tag":"m"},{"type":"direct","tag":"direct"}],
	  "experimental":{"cache_file":{"enabled":true,"path":"` + cachePath(t) + `"}}}`
	if err := c.Start([]byte(cfg)); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	wait := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			busy, known := c.GroupBusy("G")
			if !known {
				t.Fatal("内核在跑、G 是测速组:应当说得清在不在测")
			}
			if busy == want {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("等不到 GroupBusy = %v", want)
	}
	wait(false)
	done := make(chan map[string]uint16, 1)
	round := func() {
		go func() {
			res, _ := c.GroupTest(context.Background(), "G")
			done <- res
		}()
	}
	// 内核刚起来时它自己那一轮(PostStart:成员预记了"刚测过",什么都不测、瞬间就完)可能这时才开始:撞上它时 GroupTest
	// 立刻回空结果、什么都不测(daemon 的自救用例也是这样),标记只亮一下,轮询看不到。那就再叫,直到这一轮的测速请求真到了
	round()
	for deadline := time.Now().Add(5 * time.Second); ; {
		select {
		case <-arrived:
		case res := <-done:
			if len(res) != 0 {
				t.Fatalf("测速请求挂着,这一轮不该测完: %v", res)
			}
			round()
			continue
		case <-time.After(time.Until(deadline)):
			t.Fatal("这一轮的测速请求一直没到")
		}
		break
	}
	wait(true)
	if res, err := c.GroupTest(context.Background(), "G"); err != nil || len(res) != 0 {
		t.Fatalf("正在测时再叫一次:sing-box 立刻回空结果: %v %v", res, err)
	}
	release <- struct{}{} // 放行那一轮的测速请求
	if res := <-done; res["m"] == 0 {
		t.Fatalf("那一轮应真测出 m: %v", res)
	}
	wait(false)
	if _, known := c.GroupBusy("proxy"); known {
		t.Fatal("选择组(不是测速组)说不清")
	}
	if _, known := c.GroupBusy("m"); known {
		t.Fatal("单个节点说不清")
	}
	if RoundLimit(3) < 15*time.Second || RoundLimit(25) < 3*15*time.Second {
		t.Fatalf("一轮测速的上限按 sing-box 每台 15 秒、10 台一批算: %v %v", RoundLimit(3), RoundLimit(25))
	}
}
