package cli

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/profile"
)

// stdout 跑 f,返回它打到标准输出的内容。
func stdout(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	defer func() { os.Stdout = old }()
	f()
	w.Close()
	return <-done
}

// 冒烟时的订阅:8 个节点(两个只能走 IPv6),三条线路;「允许 IPv6」关着。
func smokeView() ipc.StateView {
	return ipc.StateView{
		Node:  "auto",
		Nodes: []string{"auto", "全部-Reality-主机", "全部-Reality-B", "全部-Reality-C x2", "副机-HY2-B", "副机-HY2-C x2", "主机-Trojan-WS-主机", "v6-Reality-B", "v6-Reality-C x2"},
		Groups: []profile.Group{
			{Tag: "全部-Reality", Members: []string{"全部-Reality-主机", "全部-Reality-B", "全部-Reality-C x2"}},
			{Tag: "v6-Reality", Members: []string{"v6-Reality-B", "v6-Reality-C x2"}},
		},
		V6Blocked: []string{"v6-Reality-B", "v6-Reality-C x2"},
		Delays:    map[string]int{"全部-Reality-B": 10, "v6-Reality-B": -1, "v6-Reality-C x2": -1, "副机-HY2-B": -1},
		Profile:   &ipc.ProfileView{Name: "测试订阅", NodeCount: 8},
	}
}

// 关着 IPv6 时只能走 IPv6 的节点,nodes 里要和网页界面一样标成「IPv6 节点」(文档说的是各平台都标),
// 而不是要测过一次速才显示成"不通"、也不说为什么。测速时它直接记不通(不测),那个"不通"不显示。
func TestNodeLineV6(t *testing.T) {
	v := smokeView()
	for n, want := range map[string]string{
		"v6-Reality-B":    "  v6-Reality-B  IPv6 节点(「允许 IPv6」关着,这一轮不用)",
		"v6-Reality-C x2": "  v6-Reality-C x2  IPv6 节点(「允许 IPv6」关着,这一轮不用)",
		"全部-Reality-B":    "  全部-Reality-B  10 ms",
		"副机-HY2-B":        "  副机-HY2-B  不通",
	} {
		if got := nodeLine(v, n); got != want {
			t.Fatalf("%s: 得 %q,应 %q", n, got, want)
		}
	}
	// 测过一次速之前(还没有延迟)也要标
	v.Delays = nil
	if got := nodeLine(v, "v6-Reality-B"); !strings.Contains(got, "IPv6 节点") {
		t.Fatalf("没测速时也要标: %q", got)
	}
	// 打开 IPv6 之后服务不再报它,照常显示
	v.V6Blocked = nil
	if got := nodeLine(v, "v6-Reality-B"); got != "  v6-Reality-B" {
		t.Fatalf("允许 IPv6 时不该标: %q", got)
	}
}

// status 的节点数是订阅里真正的节点数:列表首项 auto(自动选择)不是节点,不算进去 —— 以前 8 个节点的订阅显示"共 9 个",
// 下面一行"订阅:8 个节点"又对不上;界面上写的也是订阅里的节点数。关着 IPv6 时顺带说其中几个这一轮不用。
func TestStatusNodeCount(t *testing.T) {
	out := stdout(t, func() { printStatus(smokeView()) })
	line := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "节点:") {
			line = l
		}
	}
	if !strings.Contains(line, "共 8 个节点") || strings.Contains(line, "9") {
		t.Fatalf("节点数应是 8(不算 auto):%q", line)
	}
	if !strings.Contains(line, "其中 2 个是 IPv6 节点") {
		t.Fatalf("关着 IPv6 时要说其中几个这一轮不用:%q", line)
	}
	if !strings.Contains(out, "订阅:   测试订阅  8 个节点") {
		t.Fatalf("订阅那行:%s", out)
	}
	// 内核里 proxy 组的名字(订阅缓存还没有时用它):auto 和线路组都不是节点
	v := ipc.StateView{Node: "auto", Nodes: []string{"auto", "全部-Reality", "a", "b"}, Groups: []profile.Group{{Tag: "全部-Reality", Members: []string{"a", "b"}}}}
	if got := nodeTotal(v); got != 2 {
		t.Fatalf("只数节点:得 %d", got)
	}
	out = stdout(t, func() { printStatus(v) })
	if !strings.Contains(out, "共 2 个节点") || strings.Contains(out, "IPv6 节点") {
		t.Fatalf("没有 IPv6 节点时不提:%s", out)
	}
}

// nodes 后半的线路组:成员里只能走 IPv6 的标出来,成员全是这种的线路整条标(和网页界面的线路行一样)。
func TestPrintGroupsV6(t *testing.T) {
	out := stdout(t, func() { printGroups(smokeView()) })
	want := []string{
		"  全部-Reality:全部-Reality-主机 / 全部-Reality-B / 全部-Reality-C x2\n",
		"  v6-Reality:v6-Reality-B(IPv6) / v6-Reality-C x2(IPv6)  IPv6 节点(「允许 IPv6」关着,这一轮不用)\n",
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Fatalf("缺 %q:\n%s", w, out)
		}
	}
}

// test all:按节点列表的顺序打(以前按 map 的顺序,每次都乱),只能走 IPv6 的写明是没测,不写"不通"。
func TestPrintProbe(t *testing.T) {
	res := map[string]int{"v6-Reality-C x2": -1, "全部-Reality-B": 10, "v6-Reality-B": -1, "副机-HY2-B": -1, "全部-Reality-主机": 5, "新节点": 7}
	got := stdout(t, func() { printProbe(smokeView(), res) })
	want := "全部-Reality-主机: 5 ms\n" +
		"全部-Reality-B: 10 ms\n" +
		"副机-HY2-B: 不通\n" +
		"v6-Reality-B: IPv6 节点(「允许 IPv6」关着,没测)\n" +
		"v6-Reality-C x2: IPv6 节点(「允许 IPv6」关着,没测)\n" +
		"新节点: 7 ms\n"
	if got != want {
		t.Fatalf("得\n%s应\n%s", got, want)
	}
}
