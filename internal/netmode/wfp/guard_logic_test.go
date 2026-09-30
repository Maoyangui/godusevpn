//go:build windows

package wfp

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// sourceFunc 从本包某个源文件里取一个函数的函数体(到顶格的 "}" 为止)。只读源码,不碰系统。
func sourceFunc(t *testing.T, file, sig string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("读不到 %s: %v", file, err)
	}
	src := strings.ReplaceAll(string(b), "\r\n", "\n")
	i := strings.Index(src, sig)
	if i < 0 {
		t.Fatalf("%s 里找不到 %q —— 函数被改名或改签名了,这条测试要跟着更新", file, sig)
	}
	rest := src[i:]
	if j := strings.Index(rest, "\n}\n"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// 放行本服务那条是硬放行(CLEAR_ACTION_RIGHT)。只按 exe 路径的话,任何账户用同一个 exe 起的进程
// (挂起启动再注入、设 GODUSEVPN_DATA 跑 run)都命中它,从物理网卡直连。钉住:还要比进程令牌,
// 令牌要求只认 LocalSystem(服务本身)与启用状态的管理员组(提权的前台 run),只给"匹配过滤器"这一项权限。
func TestPermitSelfRequiresPrivilegedToken(t *testing.T) {
	body := sourceFunc(t, "rules.go", "func permitSelf(")
	for _, want := range []string{"cFWPM_CONDITION_ALE_APP_ID", "cFWPM_CONDITION_ALE_USER_ID", "selfUserSDDL", "pin.Pin(userID)"} {
		if !strings.Contains(body, want) {
			t.Fatalf("permitSelf 里没有 %s:放行本服务又只按 exe 路径了(或条件值没钉住)", want)
		}
	}
	aces := regexp.MustCompile(`\(([^)]*)\)`).FindAllStringSubmatch(selfUserSDDL, -1)
	if !strings.HasPrefix(selfUserSDDL, "D:") || len(aces) == 0 {
		t.Fatalf("令牌要求不是一条 DACL: %q", selfUserSDDL)
	}
	sids := map[string]bool{}
	for _, a := range aces {
		f := strings.Split(a[1], ";")
		// CC = 0x1 = FWP_ACTRL_MATCH_FILTER:WFP 判 ALE_USER_ID 时拿进程令牌对这份描述符做访问检查,要的就是这一位
		if len(f) != 6 || f[0] != "A" || f[2] != "CC" {
			t.Fatalf("ACE %q 应当是只给 CC 的允许项", a[1])
		}
		sids[f[5]] = true
	}
	if !sids["SY"] {
		t.Fatal("LocalSystem 不在名单里:服务自己连节点、拉订阅都会被自己的闸拦死")
	}
	if !sids["BA"] {
		t.Fatal("管理员组不在名单里:提权终端里的前台 run 排障模式会被自己的闸拦死")
	}
	for s := range sids {
		if s != "SY" && s != "BA" {
			t.Fatalf("%s 也能命中放行本服务:普通用户起的同一个 exe 又能直连了", s)
		}
	}
	if cFWP_ACTRL_MATCH_FILTER != 1 {
		t.Fatal("FWP_ACTRL_MATCH_FILTER 不是 0x1,SDDL 里的 CC 对不上")
	}
}

// 运行期那组的拦截要做成"否决"(CLEAR_ACTION_RIGHT):sing-tun 严格路由在它自己的最高权重子层里按 exe 路径
// 给内核进程装了硬放行,它的子层先判时,普通拦截盖不过它,同一个 exe 路径起的任何进程都能出去。
// 开机那组不带(那时没有别的子层)。
func TestBlockFlagsVetoInPersistentSet(t *testing.T) {
	saved := curFlags
	defer func() { curFlags = saved }()
	curFlags = cFWPM_FILTER_FLAG_PERSISTENT
	if f := blockFlags(); f&cFWPM_FILTER_FLAG_PERSISTENT == 0 || f&cFWPM_FILTER_FLAG_CLEAR_ACTION_RIGHT == 0 {
		t.Fatalf("运行期那组的拦截没做成否决: %#x", f)
	}
	curFlags = cFWPM_FILTER_FLAG_BOOTTIME
	if f := blockFlags(); f != cFWPM_FILTER_FLAG_BOOTTIME {
		t.Fatalf("开机那组的拦截标志被改了: %#x", f)
	}
	for file, sig := range map[string]string{"rules.go": "func blockAll(", "rules_dns.go": "func blockDNS("} {
		if !strings.Contains(sourceFunc(t, file, sig), "blockFlags()") {
			t.Fatalf("%s 的 %s 没用 blockFlags():这组拦截盖不过别的子层按 exe 路径的硬放行", file, sig)
		}
	}
}

// dnsBlocks 两条拦 DNS 的过滤器(出站 IPv4 / IPv6 各一条,带条件),标志是 flag。
func dnsBlocks(flag wtFwpmFilterFlags) []filterInfo {
	return []filterInfo{
		{layer: cFWPM_LAYER_ALE_AUTH_CONNECT_V4, name: dnsBlockName4, action: cFWP_ACTION_BLOCK, flags: flag, conds: 4},
		{layer: cFWPM_LAYER_ALE_AUTH_CONNECT_V6, name: dnsBlockName6, action: cFWP_ACTION_BLOCK, flags: flag, conds: 4},
	}
}

func TestBootGuardCoversRequiresEnabledBlockingLayer(t *testing.T) {
	layers := ourLayers()
	fs := make([]filterInfo, 0, len(layers))
	for _, layer := range layers {
		fs = append(fs, filterInfo{layer: layer, action: cFWP_ACTION_BLOCK})
	}
	fs = append(fs, dnsBlocks(0)...)
	if bootGuardCovers(fs) {
		t.Fatal("non-boot filters must not satisfy boot guard")
	}
	for i := range fs {
		fs[i].flags = cFWPM_FILTER_FLAG_BOOTTIME
	}
	if !bootGuardCovers(fs) {
		t.Fatal("all enabled blocking boot layers should satisfy boot guard")
	}
	fs[0].action = cFWP_ACTION_PERMIT
	if bootGuardCovers(fs) {
		t.Fatal("a permit filter must not satisfy boot guard")
	}
}

func TestGuardCoversRequiresEveryLayerAndFlag(t *testing.T) {
	layers := ourLayers()
	fs := make([]filterInfo, len(layers))
	for i, layer := range layers {
		fs[i] = filterInfo{layer: layer, action: cFWP_ACTION_BLOCK, flags: cFWPM_FILTER_FLAG_PERSISTENT}
	}
	fs = append(fs, dnsBlocks(cFWPM_FILTER_FLAG_PERSISTENT)...)
	if !guardCovers(fs, cFWPM_FILTER_FLAG_PERSISTENT) {
		t.Fatal("complete persistent blocking set should pass")
	}
	fs[0].flags = cFWPM_FILTER_FLAG_BOOTTIME
	if guardCovers(fs, cFWPM_FILTER_FLAG_PERSISTENT) {
		t.Fatal("missing persistent layer must fail")
	}
	fs[0].flags = cFWPM_FILTER_FLAG_PERSISTENT | cFWPM_FILTER_FLAG_DISABLED
	if guardCovers(fs, cFWPM_FILTER_FLAG_PERSISTENT) {
		t.Fatal("disabled blocking layer must fail")
	}
}

// 提供者不绑 Windows 服务名。绑了的话,开机时 BFE 会看那个服务是不是自动启动,不是就把提供者名下的
// 过滤器全部停用 —— 闸能不能跨过重启就取决于服务的启动类型(被改成手动 / 禁用、服务对象被删都会让闸
// 下次开机失效)。停服务本身不影响,已实测(见 baseProvider 的注释)。m29 的 f183df1 绑过一次,这条钉住。
func TestBaseProviderIsNotBoundToAService(t *testing.T) {
	p := baseProvider(&wtFwpmDisplayData0{})
	if p.serviceName != nil {
		t.Fatal("WFP 提供者绑上了服务名:闸能不能跨过开机就取决于服务的启动类型,服务一旦不是自动启动,下次开机 BFE 就把它名下的过滤器全部停用")
	}
	if p.flags&fwpProviderFlagPersistent == 0 {
		t.Fatal("WFP 提供者不是持久的:进程退出 / 崩溃 / 重启之后闸就没了")
	}
	if p.providerKey != providerKey {
		t.Fatal("提供者 GUID 变了:恢复命令与卸载程序都靠这个固定 GUID 找对象")
	}
}

// 两代 GUID:第二代是现役,第一代只用来认出并收掉旧对象。两代的值都钉死 ——
// 第一代改了就认不出 m29 那几版留下的对象(升级后旧闸永远留在系统里);
// 第二代改回第一代就又得走"先删光再重建"那条有空窗的路。
func TestGuardGenerations(t *testing.T) {
	d4 := [8]byte{0x9d, 0x55, 0x67, 0x6f, 0x64, 0x75, 0x73, 0x65}
	wantLegacyP := windows.GUID{Data1: 0x6f6d9e2c, Data2: 0x3a41, Data3: 0x4b8e, Data4: d4}
	wantLegacyS := windows.GUID{Data1: 0x6f6d9e2d, Data2: 0x3a41, Data3: 0x4b8e, Data4: d4}
	if legacyProviderKey != wantLegacyP || legacySublayerKey != wantLegacyS {
		t.Fatal("第一代 GUID 变了:m29 那几版(0.6.25-m29 ~ 0.7.1)留下的提供者 / 子层 / 过滤器就认不出来了")
	}
	// 第二代的值也钉死:改了它,0.7.4 起装下的 …2e / …2f 对象下一版就认不出、删不掉(断开 / 卸载后断网卡死)
	wantP := windows.GUID{Data1: 0x6f6d9e2e, Data2: 0x3a41, Data3: 0x4b8e, Data4: d4}
	wantS := windows.GUID{Data1: 0x6f6d9e2f, Data2: 0x3a41, Data3: 0x4b8e, Data4: d4}
	if providerKey != wantP || sublayerKey != wantS {
		t.Fatal("第二代 GUID 变了:0.7.4 起装下的提供者 / 子层 / 过滤器就认不出来了。要换代就再加一代,别改这一代的值")
	}
	if providerKey == legacyProviderKey || sublayerKey == legacySublayerKey {
		t.Fatal("现役 GUID 和第一代相同:换代换到同一个 GUID 上,升级又得先删光过滤器再重建,中间没有闸")
	}
	if base.provider != providerKey || base.filters != sublayerKey {
		t.Fatal("base 没指向现役这一代:新过滤器会装到旧提供者名下")
	}
	other := windows.GUID{Data1: 0x11111111}
	for _, c := range []struct {
		name string
		sub  windows.GUID
		prov *windows.GUID
		want bool
	}{
		{"现役子层", sublayerKey, nil, true},
		{"第一代子层", legacySublayerKey, nil, true},
		{"现役提供者", other, &providerKey, true},
		{"第一代提供者", other, &legacyProviderKey, true},
		{"别人的子层、没提供者", other, nil, false},
		{"别人的子层、别人的提供者", other, &other, false},
	} {
		if got := isOurs(c.sub, c.prov); got != c.want {
			t.Fatalf("isOurs(%s) = %v,想要 %v", c.name, got, c.want)
		}
	}
}

func TestJoinWarn(t *testing.T) {
	for _, c := range []struct{ a, b, want string }{
		{"", "", ""},
		{"甲", "", "甲"},
		{"", "乙", "乙"},
		{"甲", "乙", "甲;乙"},
	} {
		if got := joinWarn(c.a, c.b); got != c.want {
			t.Fatalf("joinWarn(%q, %q) = %q,想要 %q", c.a, c.b, got, c.want)
		}
	}
}

// PersistentGuardReady 查的是**运行期**那组(PERSISTENT)覆盖全不全 —— 那一组就是闸本身。
// 开机那组(BOOTTIME)只覆盖开机到 BFE 启动之间那几秒,装不上只该告警,不该让 Windows 上
// 整个进不了全局模式。m29 把两组 AND 在一起,结果"开机组少一条"= 完全连不上;
// 而修这个问题时又极容易只改注释不改函数体(第一版就是这么漏的),所以这条必须钉死。
func TestPersistentGuardReadyIgnoresBootTimeSet(t *testing.T) {
	layers := ourLayers()

	full := func(flag wtFwpmFilterFlags) []filterInfo {
		fs := make([]filterInfo, 0, len(layers))
		for _, l := range layers {
			fs = append(fs, filterInfo{layer: l, action: cFWP_ACTION_BLOCK, flags: flag})
		}
		return append(fs, dnsBlocks(flag)...)
	}

	t.Run("运行期那组齐了、开机那组一条都没有:算就绪", func(t *testing.T) {
		if !persistentGuardReady(full(cFWPM_FILTER_FLAG_PERSISTENT)) {
			t.Fatal("开机那组不齐不该影响持久保护的判定 —— 否则 Windows 上全局模式完全连不上")
		}
	})

	t.Run("运行期那组缺一层:不算就绪", func(t *testing.T) {
		fs := full(cFWPM_FILTER_FLAG_PERSISTENT)
		if persistentGuardReady(fs[1:]) {
			t.Fatal("运行期那组缺层却判成就绪:那是真的没保护")
		}
	})

	t.Run("只有开机那组:不算就绪", func(t *testing.T) {
		if persistentGuardReady(full(cFWPM_FILTER_FLAG_BOOTTIME)) {
			t.Fatal("只有开机过滤器不等于运行期有保护")
		}
	})

	t.Run("运行期那组被标成 DISABLED:不算就绪", func(t *testing.T) {
		fs := full(cFWPM_FILTER_FLAG_PERSISTENT)
		for i := range fs {
			fs[i].flags |= cFWPM_FILTER_FLAG_DISABLED
		}
		if persistentGuardReady(fs) {
			t.Fatal("被 BFE 标成 DISABLED 的过滤器不拦任何包,不能算保护")
		}
	})
}

// 持久闸拦 DNS(见 blockDNS):完整性判定要认得出三件事 ——
//   - 带条件的拦截(比如拦 DNS 那条)不能顶替"这一层全拦":否则出站层的全拦丢了也看不出来;
//   - 两条拦 DNS 缺一条就不算齐(从没有它们的旧版升上来,守护进程据此按当前设置重装);
//   - 拦 DNS 那条被停用、或者挂错了层,也不算。
func TestGuardCoversNeedsUnconditionalBlockAndDNS(t *testing.T) {
	const p = cFWPM_FILTER_FLAG_PERSISTENT
	full := func() []filterInfo {
		var fs []filterInfo
		for _, l := range ourLayers() {
			fs = append(fs, filterInfo{layer: l, action: cFWP_ACTION_BLOCK, flags: p})
		}
		return append(fs, dnsBlocks(p)...)
	}
	if !guardCovers(full(), p) {
		t.Fatal("全拦齐了、两条拦 DNS 也在,应当算齐")
	}

	t.Run("出站 IPv4 的全拦丢了、只剩拦 DNS", func(t *testing.T) {
		var fs []filterInfo
		for _, f := range full() {
			if f.layer == cFWPM_LAYER_ALE_AUTH_CONNECT_V4 && f.conds == 0 {
				continue
			}
			fs = append(fs, f)
		}
		if guardCovers(fs, p) {
			t.Fatal("拦 DNS 带条件,不能当成这一层全拦")
		}
	})

	for _, name := range []string{dnsBlockName4, dnsBlockName6} {
		t.Run("缺 "+name, func(t *testing.T) {
			var fs []filterInfo
			for _, f := range full() {
				if f.name != name {
					fs = append(fs, f)
				}
			}
			if guardCovers(fs, p) {
				t.Fatalf("少了 %s 还算齐:隧道断开时 DNS 能经局域网放行出去", name)
			}
		})
	}

	t.Run("拦 DNS 被停用", func(t *testing.T) {
		fs := full()
		for i := range fs {
			if fs[i].name == dnsBlockName4 {
				fs[i].flags |= cFWPM_FILTER_FLAG_DISABLED
			}
		}
		if guardCovers(fs, p) {
			t.Fatal("被停用的拦 DNS 不拦任何包")
		}
	})

	t.Run("拦 DNS 挂错了层", func(t *testing.T) {
		fs := full()
		for i := range fs {
			if fs[i].name == dnsBlockName6 {
				fs[i].layer = cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V6
			}
		}
		if guardCovers(fs, p) {
			t.Fatal("拦 DNS 要在出站层")
		}
	})

	t.Run("开机那组同样要求", func(t *testing.T) {
		var fs []filterInfo
		for _, l := range ourLayers() {
			fs = append(fs, filterInfo{layer: l, action: cFWP_ACTION_BLOCK, flags: cFWPM_FILTER_FLAG_BOOTTIME})
		}
		if bootGuardCovers(fs) {
			t.Fatal("开机那组没有拦 DNS 也算齐了")
		}
		if !bootGuardCovers(append(fs, dnsBlocks(cFWPM_FILTER_FLAG_BOOTTIME)...)) {
			t.Fatal("开机那组齐了却判不齐")
		}
	})
}
