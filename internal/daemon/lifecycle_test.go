package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Maoyangui/godusevpn/internal/netmode"
	"github.com/Maoyangui/godusevpn/internal/settings"
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

// handlerBody 控制口处理器(registerHandlers 里的闭包)的源码:从 h(<方法>, 到它的 "\n\t})"。
func handlerBody(t *testing.T, src, method string) string {
	t.Helper()
	return blockAfterSep(t, src, "h(ipc."+method+", func(", "\n\t})")
}

func blockAfterSep(t *testing.T, src, marker, end string) string {
	t.Helper()
	i := strings.Index(src, marker)
	if i < 0 {
		t.Fatalf("找不到 %q —— 代码改了,这条测试要跟着更新", marker)
	}
	rest := src[i:]
	if j := strings.Index(rest, end); j >= 0 {
		return rest[:j]
	}
	return rest
}

func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// 只有改了闸 / 网卡 IPv6 所依据的设置,保存时才同步它们。
func TestProtectionChanged(t *testing.T) {
	base := settings.Default()
	if protectionChanged(base, base) {
		t.Fatal("什么都没改")
	}
	for name, mut := range map[string]func(*settings.Settings){
		"日志天数": func(s *settings.Settings) { s.LogDays = 30 },
		"规则组":  func(s *settings.Settings) { s.RuleGroups = []settings.RuleGroup{{Name: "x"}} },
		"测速间隔": func(s *settings.Settings) { s.ProbeMinutes = 9 },
		"节点":   func(s *settings.Settings) { s.Selected = "hk" },
	} {
		s := base
		mut(&s)
		if protectionChanged(base, s) {
			t.Fatalf("改%s和保护无关,不该同步闸和网卡 IPv6", name)
		}
	}
	for name, mut := range map[string]func(*settings.Settings){
		"模式":        func(s *settings.Settings) { s.Mode = settings.ModeGlobal },
		"禁直连":       func(s *settings.Settings) { s.NoDirect = false },
		"局域网直通":     func(s *settings.Settings) { s.LANBypass = false },
		"网关模式":      func(s *settings.Settings) { s.NetMode = settings.NetGateway },
		"TUN":       func(s *settings.Settings) { s.TUN = false },
		"IPv6":      func(s *settings.Settings) { s.IPv6 = true },
		"停用网卡 IPv6": func(s *settings.Settings) { s.DisableNICIPv6 = false },
	} {
		s := base
		mut(&s)
		if !protectionChanged(base, s) {
			t.Fatalf("改%s会影响闸或网卡 IPv6,要同步", name)
		}
	}
}

// 服务刚起、自动连接还没接上时,"想不想连着"按落盘意愿:否则这几秒里的同步会把上次连着关的机当成不想连。
func TestWantConnectedDuringBoot(t *testing.T) {
	d := newPolicyTestDaemon(t)
	d.settings.Mode, d.settings.NoDirect = settings.ModeGlobal, true
	if d.GuardWanted() || d.nicIPv6Wanted() {
		t.Fatal("没连、也不是刚启动:不该要闸")
	}
	d.bootWanted.Store(true)
	if !d.GuardWanted() {
		t.Fatal("上次连着关的机、自动连接还没接上:闸该留着")
	}
	if d.nicIPv6Wanted() != netmode.NICIPv6Manageable() {
		t.Fatal("上次连着关的机、自动连接还没接上:网卡 IPv6 该关着")
	}
}

// 装闸那几百毫秒里(持久那组已生效、guardOn 还没置真)新来的直连动作也要按闸开着拒掉。
func TestArmingCountsAsArmed(t *testing.T) {
	d := newPolicyTestDaemon(t)
	d.cancelDirect() // applyGuard 开头
	release := d.holdDirect(func() {})
	defer release()
	if !d.guardArmed() {
		t.Fatal("正在装闸时新来的直连测速没被拦住")
	}
	d.endArming() // 装失败、原来也没闸
	if d.guardArmed() {
		t.Fatal("装完了还当闸开着")
	}
}

// 启动窗口与和保护无关的保存:这几条要真装 / 撤闸才走得到,本机不能跑,钉源码结构。
func TestGuardSyncWiring(t *testing.T) {
	src := readDaemonSource(t)
	run := funcBody(t, src, "func (d *Daemon) Run(ctx context.Context) error {")
	before(t, run, "netmode.GuardStatus()", "d.server.Listen()", "控制口开之前按实物把闸记成开着")
	before(t, run, "d.connOpMu.Lock()", "d.machine.Connect()", "自动连接和断开串行,锁里重读落盘意愿")
	before(t, run, "d.machine.Connect()", "d.bootWanted.Store(false)", "自动连接接上之后才把意愿交给状态机")
	disc := handlerBody(t, src, "MDisconnect")
	before(t, disc, "d.bootWanted.Store(false)", "d.syncGuard()", "刚启动时点的断开也要能撤闸")
	set := handlerBody(t, src, "MSetSettings")
	before(t, set, "if protectionChanged(prev, next) {\n\t\t\td.syncGuard()", "d.syncNICIPv6()", "和保护无关的保存不同步闸与网卡 IPv6")
	if strings.Count(set, "d.syncGuard()") != 1 {
		t.Fatal("MSetSettings 里只该有 protectionChanged 下那一处 syncGuard")
	}
	mode := handlerBody(t, src, "MSetMode")
	if !strings.Contains(mode, "if s.Mode != prev.Mode {") || strings.Index(mode, "if s.Mode != prev.Mode {") > strings.LastIndex(mode, "d.syncGuard()") {
		t.Fatal("再点一次当前模式不该同步闸与网卡 IPv6")
	}
	for fn, body := range map[string]string{
		"syncGuard":   funcBody(t, readSource(t, "refresh.go"), "func (d *Daemon) syncGuard() {"),
		"syncNICIPv6": funcBody(t, src, "func (d *Daemon) syncNICIPv6() error {"),
	} {
		if !strings.Contains(body, "!d.settingsTrusted() && d.wantConnected()") {
			t.Fatalf("%s:设置读不出来时只在想连着时保持现状,明确断开要照常撤", fn)
		}
	}
}

// 严格全局下内核没起来时拉订阅只能直连;缓存能用就先用缓存连上,再经"当前代理 → auto → 直连"刷新。
func TestRefreshBeforeStart(t *testing.T) {
	for _, c := range []struct {
		name                   string
		cacheOK, stale, strict bool
		want                   bool
	}{
		{"没有能用的缓存(首次 / 改了地址):只能先拉", false, false, true, true},
		{"严格全局,缓存过期:先用缓存连上,连上后经代理刷新", true, true, true, false},
		{"严格全局,缓存新鲜", true, false, true, false},
		{"非严格,缓存过期:照旧先刷新", true, true, false, true},
		{"非严格,缓存新鲜", true, false, false, false},
	} {
		if got := refreshBeforeStart(c.cacheOK, c.stale, c.strict); got != c.want {
			t.Fatalf("%s:得 %v,应 %v", c.name, got, c.want)
		}
	}
	src := readDaemonSource(t)
	if !strings.Contains(funcBody(t, src, "func (d *Daemon) prepare(ctx context.Context) ([]byte, error) {"), "refreshBeforeStart(") {
		t.Fatal("prepare 要按 refreshBeforeStart 决定起内核前拉不拉订阅")
	}
	before(t, funcBody(t, src, "func (d *Daemon) start(cfg []byte) error {"), "d.core.Start(cfg)", "go d.maybeRefresh(", "连上之后要补上跳过的那次刷新")
}

// 临时换线借过去之后不会自己切回(用户还没拍板要不要切回):日志不能承诺"恢复后自动用回去"。
func TestRecoverRouteLogPromisesNothing(t *testing.T) {
	fn := funcBody(t, readSource(t, "startfail.go"), "func (d *Daemon) recoverRoute(ctx context.Context) bool {")
	if strings.Contains(fn, "自动用回") {
		t.Fatal("没有切回逻辑,日志不能说恢复后会自动用回去")
	}
}

// 闸开着、但平台这一层保护不完整(Android 没开 lockdown 等)时,界面要能标出来,不能照样亮绿盾。
func TestStateViewCarriesGuardNote(t *testing.T) {
	d := newPolicyTestDaemon(t)
	d.guardOn = true
	d.guardNote, d.guardNoteAt = "系统的「阻止未经 VPN 的连接」没开", time.Now()
	if v := d.stateView(); v.Guard != "on" || v.GuardNote != d.guardNote {
		t.Fatalf("闸开着且平台有保留:应带上 guardNote,得 %q / %q", v.Guard, v.GuardNote)
	}
	d.guardErr = "转发层没放行"
	if v := d.stateView(); v.GuardNote != "" {
		t.Fatal("已经有 guardError 时不再叠一句")
	}
	d.guardOn, d.guardErr = false, ""
	if v := d.stateView(); v.GuardNote != "" {
		t.Fatal("闸没开时不该带 guardNote")
	}
}
