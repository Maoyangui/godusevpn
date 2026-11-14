package uiapi

import (
	"encoding/json"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/ipc"
)

// recordBackend 记下页面方法最终调到了服务的哪个控制口方法、带了什么参数。
type recordBackend struct {
	method string
	params string
}

func (b *recordBackend) Dispatch(method string, params json.RawMessage) (any, error) {
	b.method, b.params = method, string(params)
	return ipc.StateView{Node: "香港1"}, nil
}
func (*recordBackend) Logf(string, ...any) {}

// 节点面板的「线路自动」开关:Android 与 Linux 网页面板都经这里调到服务的 SetLineAuto,返回新状态给页面立刻重画。
func TestSetLineAutoReachesService(t *testing.T) {
	b := &recordBackend{}
	res, err := New(b, Options{}).Call("SetLineAuto", []json.RawMessage{json.RawMessage(`true`)})
	if err != nil {
		t.Fatal(err)
	}
	if b.method != ipc.MSetLineAuto || b.params != `{"on":true}` {
		t.Fatalf("应调服务的 %s {on:true},实际 %s %s", ipc.MSetLineAuto, b.method, b.params)
	}
	if v, ok := res.(ipc.StateView); !ok || v.Node != "香港1" {
		t.Fatalf("应把服务返回的状态交给页面: %#v", res)
	}
}
