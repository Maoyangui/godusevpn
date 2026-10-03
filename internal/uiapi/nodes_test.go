package uiapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/ipc"
)

// connectedBackend 内核在跑:Clash API 指向测试里起的假内核。
type connectedBackend struct{ port int }

func (b connectedBackend) Dispatch(method string, _ json.RawMessage) (any, error) {
	switch method {
	case ipc.MGetClashInfo:
		return ipc.ClashInfo{Port: b.port, Secret: "s", Running: true}, nil
	case ipc.MGetState:
		return ipc.StateView{Nodes: []string{"auto", "香港1", "英国warp", "老节点"}, Unsupported: map[string]string{"老节点": "unknown outbound type: naive"}}, nil
	}
	return nil, nil
}
func (connectedBackend) Logf(string, ...any) {}

// 连着时节点列表按订阅缓存列:刷新新加的(内核里还没有)也列出来、标成待用上,不显示延迟;订阅已删、内核里还留着的不列。
// 以前只列内核 proxy 组里的,订阅新加的线路要等重连才看得见(刷新日志却写着"已更新到节点列表")。
func TestNodesListRefreshAddedWhileConnected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxies" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"proxies":{
			"proxy":{"name":"proxy","type":"Selector","now":"香港1","all":["auto","香港1","已删节点"]},
			"auto":{"name":"auto","type":"URLTest","now":"香港1"},
			"香港1":{"name":"香港1","type":"AnyTLS","history":[{"delay":80}]},
			"已删节点":{"name":"已删节点","type":"Hysteria2"}}}`))
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	nodes, err := New(connectedBackend{port: port}, Options{}).nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	by := map[string]NodeInfo{}
	for _, n := range nodes {
		names = append(names, n.Name)
		by[n.Name] = n
	}
	if strings.Join(names, ",") != "auto,香港1,英国warp,老节点" {
		t.Fatalf("要按订阅缓存列: %v", names)
	}
	if h := by["香港1"]; h.Type != "AnyTLS" || h.Delay != 80 || !h.Current || h.Pending {
		t.Fatalf("内核里有的照用内核的信息: %+v", h)
	}
	if w := by["英国warp"]; !w.Pending || w.Unsupported != "" || w.Delay != 0 {
		t.Fatalf("刷新新加的要标成待用上: %+v", w)
	}
	if o := by["老节点"]; o.Pending || o.Unsupported == "" {
		t.Fatalf("内核建不起来的标原因,不算待用上: %+v", o)
	}
}

type nodesBackend struct{}

func (nodesBackend) Dispatch(method string, _ json.RawMessage) (any, error) {
	if method == ipc.MGetState {
		return ipc.StateView{Nodes: []string{"auto", "香港1", "老节点"}, Unsupported: map[string]string{"老节点": "unknown outbound type: naive"}}, nil
	}
	return nil, nil // 内核没在跑:节点列表按订阅给
}
func (nodesBackend) Logf(string, ...any) {}

// 内核建不起来、这次连接跳过的节点要在节点列表里标出来(带原因),而不是只写日志、选了它其实在用自动选择。
func TestNodesMarkUnsupported(t *testing.T) {
	nodes, err := New(nodesBackend{}, Options{}).nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, n := range nodes {
		got[n.Name] = n.Unsupported
	}
	if len(nodes) != 3 || got["老节点"] == "" || got["香港1"] != "" || got["auto"] != "" {
		t.Fatalf("只有内核建不起来的那个带原因: %+v", nodes)
	}
}
