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

// changedBackend 面板把「香港1」从 hysteria2 换成了 vless,客户端刷新过了、当前节点没受影响所以内核没重建:
// 状态里的协议(订阅缓存)是 vless,内核报的还是 Hysteria2。
type changedBackend struct{ port int }

func (b changedBackend) Dispatch(method string, _ json.RawMessage) (any, error) {
	switch method {
	case ipc.MGetClashInfo:
		if b.port == 0 {
			return nil, nil
		}
		return ipc.ClashInfo{Port: b.port, Secret: "s", Running: true}, nil
	case ipc.MGetState:
		return ipc.StateView{
			Nodes:   []string{"auto", "香港1", "香港3", "新加的"},
			Types:   map[string]string{"香港1": "vless", "香港3": "anytls", "新加的": "vless"},
			Changed: []string{"香港1"},
			Delays:  map[string]int{"香港3": 66},
		}, nil
	}
	return nil, nil
}
func (changedBackend) Logf(string, ...any) {}

// 节点列表上的协议以刷新到的订阅为准,不以内核为准;参数变了、内核里还是旧的那份的节点标出来,内核给它记的延迟不用。
// 用户真机:几十条线路从 hysteria2 换成 vless,刷新多次,列表上还全是 HYSTERIA2。
func TestNodesTypeFollowsSubscriptionNotKernel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"proxies":{
			"proxy":{"name":"proxy","type":"Selector","now":"香港3","all":["auto","香港1","香港3"]},
			"auto":{"name":"auto","type":"URLTest","now":"香港3"},
			"香港1":{"name":"香港1","type":"Hysteria2","history":[{"delay":198}]},
			"香港3":{"name":"香港3","type":"AnyTLS","history":[{"delay":80}]}}}`))
	}))
	defer srv.Close()
	nodes, err := New(changedBackend{port: srv.Listener.Addr().(*net.TCPAddr).Port}, Options{}).nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]NodeInfo{}
	for _, n := range nodes {
		by[n.Name] = n
	}
	if h := by["香港1"]; h.Type != "vless" || !h.Changed || h.Delay != 0 || h.Pending {
		t.Fatalf("换了协议的:标签按订阅写 vless、标成已更新、不用内核记的旧延迟: %+v", h)
	}
	if h := by["香港3"]; h.Type != "anytls" || h.Changed || h.Delay != 66 || !h.Current {
		t.Fatalf("没变的照常: %+v", h)
	}
	if n := by["新加的"]; n.Type != "vless" || !n.Pending || n.Changed {
		t.Fatalf("刷新新加的也带协议: %+v", n)
	}
	if a := by["auto"]; a.Type != "URLTest" || a.AutoNow != "香港3" {
		t.Fatalf("内核自己的组,订阅里没有它的协议:用内核报的: %+v", a)
	}
	// 没连着时也有协议标签(以前内核不在跑就一个标签都没有)
	off, err := New(changedBackend{}, Options{}).nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range off {
		if n.Name == "香港1" && n.Type != "vless" {
			t.Fatalf("没连着时协议也按订阅写: %+v", n)
		}
	}
}
