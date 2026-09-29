package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/state"
)

// before 断言 fn 里 a 出现在 b 之前(两者都得在)。
func before(t *testing.T, fn, a, b, why string) {
	t.Helper()
	i, j := strings.Index(fn, a), strings.Index(fn, b)
	if i < 0 || j < 0 || i > j {
		t.Fatalf("%s 应出现在 %s 之前:%s", a, b, why)
	}
}

// startTestCore 给测试守护进程起一个只有出站的内核:当前节点是连不上的本机端口,不往外发包、不碰路由。
func startTestCore(t *testing.T, d *Daemon, mode string) {
	t.Helper()
	cache := filepath.ToSlash(filepath.Join(t.TempDir(), "cache.db"))
	cfg := `{"log":{"level":"error"},
"outbounds":[{"type":"selector","tag":"proxy","outbounds":["hk1"]},{"type":"socks","tag":"hk1","server":"127.0.0.1","server_port":1},{"type":"direct","tag":"direct"}],
"route":{"rules":[{"clash_mode":"Direct","outbound":"direct"},{"clash_mode":"Global","outbound":"proxy"}],"final":"proxy"},
"experimental":{"clash_api":{"default_mode":"` + mode + `"},"cache_file":{"enabled":true,"path":"` + cache + `"}}}`
	if err := d.core.Start([]byte(cfg)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.core.Stop() })
}

// 直连模式下流量不经节点:节点不通不能算线路坏了(否则每几分钟重建一次内核、打断直连流量)。
func TestHealthIgnoresNodeInDirectMode(t *testing.T) {
	d := newPolicyTestDaemon(t) // 规则模式设置、没点连接:隐私核查不碰系统
	startTestCore(t, d, "Rule")
	if err := d.health(context.Background()); state.CodeOf(err) != state.CodeNodeDown {
		t.Fatalf("前提:规则模式下节点不通应报 %s,得到 %v", state.CodeNodeDown, err)
	}
	_ = d.core.Stop()
	startTestCore(t, d, "Direct")
	if err := d.health(context.Background()); err != nil {
		t.Fatalf("直连模式下节点不通不该算不健康: %v", err)
	}
}

// blockAfter fn 里从 marker 起到这个 if 块结束(第一个同级的 "\n\t}")为止。
func blockAfter(t *testing.T, fn, marker string) string {
	t.Helper()
	i := strings.Index(fn, marker)
	if i < 0 {
		t.Fatalf("找不到 %q —— 代码改了,这条测试要跟着更新", marker)
	}
	rest := fn[i:]
	if j := strings.Index(rest, "\n\t}"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// 闸装不上 / 查不清时不能拒绝起数据面:不起隧道,流量就全部从物理网卡直连;起着 strict_route 的隧道只会更少。
// 启动前只让网卡 IPv6 类(确证在漏、停用没做成)挡连接;起来之后核查没过也不拆隧道,叫状态机马上检查、原地修。
// 这几处要真起内核、真装闸才能走到,不能在装着产品的机器上跑,只能钉源码结构。
func TestGuardFailureDoesNotBlockDataPlane(t *testing.T) {
	src := readDaemonSource(t)
	prep := funcBody(t, src, "func (d *Daemon) prepare(ctx context.Context) ([]byte, error) {")
	if strings.Contains(prep, "ensurePrivacyReady()") || !strings.Contains(prep, "d.nicReady()") {
		t.Fatal("prepare 启动前只该用网卡类核查挡连接")
	}
	if strings.Contains(blockAfter(t, prep, "d.guardReady()"), "return") {
		t.Fatal("prepare:闸类核查没过只能记下来,不能拒绝起数据面")
	}
	start := funcBody(t, src, "func (d *Daemon) start(cfg []byte) error {")
	before(t, start, "d.nicReady()", "d.core.Start(cfg)", "启动前只该用网卡类核查挡连接")
	if strings.Index(start, "ensurePrivacyReady()") < strings.Index(start, "d.core.Start(cfg)") {
		t.Fatal("start:内核起来之前不该再跑包含闸类的完整核查")
	}
	post := blockAfter(t, start, "d.ensurePrivacyReady()")
	if strings.Contains(post, "core.Stop") || strings.Contains(post, "return") || !strings.Contains(post, "CheckNow") {
		t.Fatal("start:起来之后核查没过不能拆隧道,要叫状态机马上检查、原地修")
	}
}

// 内核启动时先读缓存里记着的模式与选中节点、盖过配置(core.PresetCache 那条测试钉着 sing-box 的这个行为)。
// 守护进程必须在起内核之前把缓存对齐到这一轮配置,不能只在起来之后再 Select —— 那时 TUN 已经在收流量了。
func TestStartPresetsCacheBeforeCore(t *testing.T) {
	fn := funcBody(t, readDaemonSource(t), "func (d *Daemon) start(cfg []byte) error {")
	before(t, fn, "core.PresetCache(", "d.core.Start(cfg)", "缓存里的旧模式 / 旧节点会在内核启动时盖过配置")
}
