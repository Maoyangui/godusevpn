package uiapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/ipc"
)

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
