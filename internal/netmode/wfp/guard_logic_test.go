//go:build windows

package wfp

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestBootGuardCoversRequiresEnabledBlockingLayer(t *testing.T) {
	layers := ourLayers()
	fs := make([]filterInfo, 0, len(layers))
	for _, layer := range layers {
		fs = append(fs, filterInfo{layer: layer, action: cFWP_ACTION_BLOCK})
	}
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
		return fs
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
