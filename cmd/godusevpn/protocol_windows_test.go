package main

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

// 用一个一次性的协议名验证注册逻辑:写得进去、读回来是本程序、重复调用不出错;跑完删干净。
func TestEnsureSchemeWritesRegistry(t *testing.T) {
	const scheme = "godusevpn-selftest"
	base := `Software\Classes\` + scheme
	t.Cleanup(func() {
		for _, k := range []string{base + `\shell\open\command`, base + `\shell\open`, base + `\shell`, base + `\DefaultIcon`, base} {
			_ = registry.DeleteKey(registry.CURRENT_USER, k)
		}
	})
	exe := `C:\Program Files\godusevpn\godusevpn.exe`
	ensureScheme(scheme, exe)
	ensureScheme(scheme, exe) // 第二次应当直接认出已经对上,不该出错

	got, err := readString(base+`\shell\open\command`, "")
	if err != nil {
		t.Fatalf("读不到 open command: %v", err)
	}
	if want := `"` + exe + `" "%1"`; got != want {
		t.Fatalf("命令行不对\n实际: %s\n期望: %s", got, want)
	}
	if v, err := readString(base, "URL Protocol"); err != nil || v != "" {
		t.Fatalf("缺少 URL Protocol 标记: %q %v", v, err)
	}
	if v, _ := readString(base, ""); v != "URL:"+scheme+" Protocol" {
		t.Fatalf("默认值不对: %q", v)
	}
	// 换了安装位置要跟着更新
	exe2 := `D:\godusevpn\godusevpn.exe`
	ensureScheme(scheme, exe2)
	if got, _ := readString(base+`\shell\open\command`, ""); got != `"`+exe2+`" "%1"` {
		t.Fatalf("换路径后没更新: %s", got)
	}
}
