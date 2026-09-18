package paths

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 数据目录里有节点凭据、订阅地址和面板密码哈希。%ProgramData% 默认会把
// BUILTIN\Users:(OI)(CI)(RX) 继承下来 —— 同机任意标准用户都能直接读走,而且目录上还带着
// "创建文件"的权限:非管理员可以往 rulesets\ 里新建一个 geosite-cn.srs,静默改掉以 SYSTEM
// 身份运行的内核认定的分流规则(那一层的查找优先级最高,见 internal/ruleset)。
//
// 这条测试用 icacls 核对真实落到目录上的权限,而不是只看函数有没有返回 error ——
// 早先那句"安装时设 ACL"的注释就是在替一段根本不存在的代码背书。
func TestSecureDataDirRemovesInheritedAccess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "godusevpn")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := secureDataDir(dir); err != nil {
		t.Skipf("收紧权限失败(多半是没有管理员身份,CI 上跳过): %v", err)
	}
	out, err := exec.Command("icacls", dir).CombinedOutput()
	if err != nil {
		t.Skipf("跑不了 icacls: %v", err)
	}
	// icacls 会把目录路径原样打在第一行,而临时目录本身就在 C:\Users\… 下 ——
	// 不把路径抠掉,下面那几个名字判断会被路径里的字样误伤(第一次写就踩了)。
	got := strings.ReplaceAll(string(out), dir, "<目录>")
	// 继承标记 (I) 一条都不该剩:不断继承的话,加再多 ACE 也盖不掉继承来的 Users 读权限
	if strings.Contains(got, "(I)") {
		t.Errorf("继承没断掉,继承来的权限还在:\n%s", got)
	}
	for _, who := range []string{`\Users`, "Everyone", "Authenticated Users"} {
		if strings.Contains(got, who) {
			t.Errorf("%s 还能访问数据目录,里面是节点凭据:\n%s", who, got)
		}
	}
	if !strings.Contains(got, "SYSTEM") {
		t.Errorf("SYSTEM 必须保留完全控制,否则以 SYSTEM 跑的服务自己都写不进去:\n%s", got)
	}
}
