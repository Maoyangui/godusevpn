package daemon

import (
	"encoding/json"
	"net"
	"reflect"
	"sync"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 测速结果交给控制口时是在锁外序列化(遍历)的。守护进程自己留的那份(d.delays)要是和它是同一个 map,
// 下一次测速每测出一个就往 d.delays 里写 —— 一边遍历一边写是不可恢复的 fatal error,服务进程直接退出。
// 界面连点两次「测速」、或界面和命令行同时测,就会撞上。
func TestProbeNodesResultNotSharedWithDelays(t *testing.T) {
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := dead.Addr().(*net.TCPAddr).Port
	dead.Close() // 没人听的回环端口:握手立刻被拒,每个节点都会走到"测出一个写一个"
	var nodes []string
	for _, tag := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		nodes = append(nodes, `{"type":"trojan","tag":"`+tag+`","server":"127.0.0.1","server_port":`+itoa(port)+`,"password":"p"}`)
	}
	d := newPolicyTestDaemon(t)
	d.settings.Profiles = []settings.Profile{{ID: "p1", Name: "测试", URL: "https://example.invalid/sub"}}
	d.settings.ActiveProfile = "p1"
	d.profiles["p1"] = prof(t, nodes...)

	res, err := d.server.Dispatch(ipc.MProbeNodes, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := res.(map[string]int)
	if !ok || len(m) != len(nodes) {
		t.Fatalf("测速结果不对:%T %v", res, res)
	}
	d.mu.Lock()
	same := reflect.ValueOf(m).Pointer() == reflect.ValueOf(d.delays).Pointer()
	d.mu.Unlock()
	if same {
		t.Fatal("返回的测速结果和 d.delays 是同一个 map:控制口在锁外序列化它时,下一次测速往里写就是并发读写 map")
	}

	// 再真的并发跑一遍:一边序列化上一次的结果,一边连着测。共用 map 时 -race 会直接报,不带 -race 也常常
	// 撞上运行时的"concurrent map iteration and map write"。
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			if _, err := json.Marshal(m); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for i := 0; i < 10; i++ {
		if _, err := d.server.Dispatch(ipc.MProbeNodes, nil); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
