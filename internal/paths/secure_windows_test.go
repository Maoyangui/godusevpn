package paths

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// 数据目录里有节点凭据、订阅地址和面板密码哈希。%ProgramData% 默认会把
// BUILTIN\Users:(OI)(CI)(RX) 继承下来 —— 同机任意标准用户都能直接读走,而且目录上还带着
// "创建文件"的权限:非管理员可以往 rulesets\ 里新建一个 geosite-cn.srs,静默改掉以 SYSTEM
// 身份运行的内核认定的分流规则(那一层的查找优先级最高,见 internal/ruleset)。
//
// 这条测试用 icacls 核对真实落到目录上的权限,而不是只看函数有没有返回 error ——
// 早先那句"安装时设 ACL"的注释就是在替一段根本不存在的代码背书。
func TestSecureDataDirRemovesInheritedAccess(t *testing.T) {
	dir := setupDataDir(t)
	if err := Harden(); err != nil {
		t.Skipf("收紧权限失败(多半是没有管理员身份,CI 上跳过): %v", err)
	}
	got := icacls(t, dir)
	// 继承标记 (I) 一条都不该剩:不断继承的话,加再多 ACE 也盖不掉继承来的 Users 读权限
	if strings.Contains(got, "(I)") {
		t.Errorf("继承没断掉,继承来的权限还在:\n%s", got)
	}
	for _, who := range []string{usersACE, "Everyone", "Authenticated Users"} {
		if strings.Contains(got, who) {
			t.Errorf("%s 还能访问数据目录,里面是节点凭据:\n%s", who, got)
		}
	}
	if !strings.Contains(got, "SYSTEM") {
		t.Errorf("SYSTEM 必须保留完全控制,否则以 SYSTEM 跑的服务自己都写不进去:\n%s", got)
	}
}

// 收紧不能把界面自己的活路堵死:界面是以**登录用户**身份跑的(非提权),它要把服务生成的
// 诊断包复制到桌面,还要用资源管理器打开日志目录 —— 而诊断包恰恰是出问题时唯一的求助通道。
// 但只给登记的控制用户:0.7.5 给的是 BUILTIN\Users,内核日志里每条连接的目标域名就是一份浏览记录,
// 同机别的账户都能读走(审计 G050)。
func TestLogsAndDiagReadableOnlyByControllers(t *testing.T) {
	setupDataDir(t)
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	me := tu.User.Sid
	account, domain, _, err := me.LookupAccount("")
	if err != nil {
		t.Skipf("查不到当前账户名: %v", err)
	}
	who := domain + `\` + account
	if err := Harden(me.String()); err != nil {
		t.Skipf("收紧权限失败(多半是没有管理员身份): %v", err)
	}
	for _, sub := range []string{Logs(), Diag()} {
		got := icacls(t, sub)
		if !strings.Contains(got, who) {
			t.Errorf("%s 控制用户读不了 —— 界面复制不出诊断包、也打不开日志:\n%s", sub, got)
		}
		if strings.Contains(got, usersACE) {
			t.Errorf("%s 还对所有标准用户开放:\n%s", sub, got)
		}
		// 父目录已经断了继承,这两个必须自己带一份权限,不能靠继承
		if strings.Contains(got, "(I)") {
			t.Errorf("%s 还在继承父目录的权限,父目录一收紧它就跟着进不去了:\n%s", sub, got)
		}
	}
	// 根目录只有 SYSTEM 与管理员:凭据在那儿,控制用户也只能经服务
	if root := icacls(t, DataDir()); strings.Contains(root, usersACE) || strings.Contains(root, who+":") {
		t.Errorf("数据目录根对普通用户开放:\n%s", root)
	}
}

// 格式不对的 SID 不能拼进描述符:一条坏的会让整份解析失败,连 SYSTEM 与管理员那两条也设不上。
func TestReadableSDDLSkipsBadSIDs(t *testing.T) {
	got := readableSDDL([]string{"S-1-5-21-1-2-3-1001", "S-1-5-x", ")(A;;GA;;;WD"})
	if want := sddlPrivate + "(A;OICI;GRGX;;;S-1-5-21-1-2-3-1001)"; got != want {
		t.Fatalf("得到 %s,应为 %s", got, want)
	}
	if _, err := windows.SecurityDescriptorFromString(got); err != nil {
		t.Fatal(err)
	}
	if readableSDDL(nil) != sddlPrivate {
		t.Fatal("没有控制用户时只留 SYSTEM 与管理员")
	}
}

// usersACE icacls 输出里 BUILTIN\Users 那条的样子。
const usersACE = `BUILTIN\Users`

func setupDataDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "godusevpn")
	t.Setenv("GODUSEVPN_DATA", dir)
	t.Setenv("GODUSEVPN_CONF", dir)
	if err := Ensure(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// icacls 读一个目录的权限。把路径本身从输出里抠掉:临时目录就在 C:\Users\… 下,
// 不抠的话下面那些按名字做的判断会被路径里的字样误伤(第一次写就踩了)。
func icacls(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("icacls", dir).CombinedOutput()
	if err != nil {
		t.Skipf("跑不了 icacls: %v", err)
	}
	return strings.ReplaceAll(string(out), dir, "<目录>")
}
