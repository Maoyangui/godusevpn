package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/ruleset"
)

// 上一轮审计里最扎眼的一条是「internal/ruleset 整个包在生产代码里是死的」——
// 包写好了、测试也齐,但没有任何一处生产代码调用它,于是三个内置规则集永远落不了盘。
// 这条测试就守着这根线:守护进程一装配起来,内置规则集必须已经在数据目录里了。
//
// 只建 Daemon,不 Run:不监听控制口、不起内核、不碰任何系统网络设置。
func TestNewInstallsBuiltinRuleSets(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GODUSEVPN_DATA", dir)
	t.Setenv("GODUSEVPN_CONF", dir)

	d, err := NewWithOptions(Options{NoListen: true})
	if err != nil {
		t.Fatalf("装配守护进程失败: %v", err)
	}
	_ = d

	names := ruleset.Builtin()
	if len(names) == 0 {
		t.Fatal("内置清单是空的")
	}
	for _, tag := range names {
		p := filepath.Join(dir, "rulesets", "builtin", tag+".srs")
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("内置规则集 %s 没落盘(%s)—— 这正是「包写了但没人调」那个毛病: %v", tag, p, err)
		}
		if st.Size() == 0 {
			t.Fatalf("内置规则集 %s 落盘了但是空文件", tag)
		}
		// 三层查找也要真能找到它
		got, ok := ruleset.Find(filepath.Join(dir, "rulesets"), tag)
		if !ok || got != p {
			t.Fatalf("查找 %s 没落到内置那一层:got=%q ok=%v", tag, got, ok)
		}
	}
}
