//go:build windows

package wfp

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 真机测试:要管理员权限,会往系统里装一套持久过滤器再删掉。默认跳过,GODUSEVPN_WFP_LIVE=1 才跑。
// 放行的"本进程"填现役服务的 exe(GODUSEVPN_WFP_SELF),这样测试期间正在跑的隧道不受影响。
func TestLivePersistentGuard(t *testing.T) {
	if os.Getenv("GODUSEVPN_WFP_LIVE") != "1" {
		t.Skip("GODUSEVPN_WFP_LIVE=1 才跑(要管理员,会动系统的过滤器)")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("不是管理员")
	}
	spec := Spec{LAN: true, Tun4: [4]byte{172, 19, 0, 1}, Tun6: [16]byte{0xfd, 0xfe, 0xdc, 0xba, 0x98, 0x76, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, SelfPath: os.Getenv("GODUSEVPN_WFP_SELF")}
	// 先清干净(上次测试可能留下的)
	if err := Disable(); err != nil {
		t.Fatalf("清理: %v", err)
	}
	defer Disable()

	warn, err := Enable(spec)
	if err != nil {
		t.Fatalf("开闸: %v", err)
	}
	t.Logf("开机那组: %s", map[bool]string{true: "装上了", false: "警告:" + warn}[warn == ""])
	n1, err := Count()
	if err != nil || n1 == 0 {
		t.Fatalf("开闸后应有过滤器,得 %d %v", n1, err)
	}
	t.Logf("开闸后过滤器: %d 条", n1)

	// netsh 里看:我们的过滤器要带 persistent,开机那组带 boottime
	out, _ := exec.Command("netsh", "wfp", "show", "filters", "file=-").Output()
	xml := string(out)
	ours := strings.Count(xml, "Permit tunnel address (IPv4)")
	persistent := strings.Count(xml, "<item>FWPM_FILTER_FLAG_PERSISTENT</item>")
	boottime := strings.Count(xml, "<item>FWPM_FILTER_FLAG_BOOTTIME</item>")
	t.Logf("netsh: 隧道地址放行 %d 条(出站+入站,持久+开机应为 4),persistent 标志 %d 条,boottime 标志 %d 条", ours, persistent, boottime)
	if ours == 0 {
		t.Fatal("netsh 里看不到我们的过滤器")
	}
	if persistent == 0 {
		t.Fatal("过滤器没带 PERSISTENT 标志,进程一退就没了")
	}

	// 再开一次:幂等,数量不变
	if _, err := Enable(spec); err != nil {
		t.Fatalf("再开: %v", err)
	}
	n2, _ := Count()
	if n2 != n1 {
		t.Fatalf("重复开闸不该重复加过滤器:%d → %d", n1, n2)
	}

	// 撤闸:归零,提供者与子层也删掉
	if err := Disable(); err != nil {
		t.Fatalf("撤闸: %v", err)
	}
	n3, _ := Count()
	if n3 != 0 {
		t.Fatalf("撤闸后应归零,得 %d", n3)
	}
	// 按固定 GUID 查(按名字查会撞上正在跑的旧版服务那个动态会话的提供者);两代都不该留下
	out, _ = exec.Command("netsh", "wfp", "show", "state", "file=-").Output()
	state := strings.ToLower(string(out))
	for _, g := range []struct{ id, what string }{
		{"6f6d9e2e-3a41-4b8e-9d55-676f64757365", "提供者"}, {"6f6d9e2f-3a41-4b8e-9d55-676f64757365", "子层"},
		{"6f6d9e2c-3a41-4b8e-9d55-676f64757365", "第一代提供者"}, {"6f6d9e2d-3a41-4b8e-9d55-676f64757365", "第一代子层"},
	} {
		if strings.Contains(state, g.id) {
			t.Fatalf("撤闸后%s还在", g.what)
		}
	}
	t.Log("撤闸后过滤器 0 条,提供者已删")
}

// TestLiveEnableKeep 只开闸不撤(GODUSEVPN_WFP_KEEP=1 才跑):给手工验证「服务不在时闸还在、
// guard clear 能撤」用。跑完记得 godusevpn-svc guard clear。
func TestLiveEnableKeep(t *testing.T) {
	if os.Getenv("GODUSEVPN_WFP_LIVE") != "1" || os.Getenv("GODUSEVPN_WFP_KEEP") != "1" {
		t.Skip("GODUSEVPN_WFP_LIVE=1 GODUSEVPN_WFP_KEEP=1 才跑")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("不是管理员")
	}
	spec := Spec{LAN: true, Tun4: [4]byte{172, 19, 0, 1}, Tun6: [16]byte{0xfd, 0xfe, 0xdc, 0xba, 0x98, 0x76, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}, SelfPath: os.Getenv("GODUSEVPN_WFP_SELF")}
	warn, err := Enable(spec)
	if err != nil {
		t.Fatalf("开闸: %v", err)
	}
	n, _ := Count()
	t.Logf("开闸(不撤): %d 条, warn=%q", n, warn)
}

// TestLiveSelfPermitNeedsPrivilegedToken 放行本服务按 exe 路径 + 进程令牌:把 curl 复制一份当"本服务",
// 提权的它绑物理网卡直连应当放行(前台 run 排障模式靠这个),同一个 exe 换成管理员组只能用来拒绝的令牌
// (等同没提权 / 普通用户)必须被拦,别的 exe 照样被拦。要管理员;期间本机除它以外的直连全拦几秒。
func TestLiveSelfPermitNeedsPrivilegedToken(t *testing.T) {
	if os.Getenv("GODUSEVPN_WFP_LIVE") != "1" {
		t.Skip("GODUSEVPN_WFP_LIVE=1 才跑(要管理员,会动系统的过滤器)")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("不是管理员")
	}
	c, err := net.Dial("udp4", "1.1.1.1:80") // 只查路由,不发包
	if err != nil {
		t.Skipf("没有 IPv4 默认路由: %v", err)
	}
	phys := c.LocalAddr().(*net.UDPAddr).IP.String()
	c.Close()
	sysCurl := filepath.Join(os.Getenv("SystemRoot"), "System32", "curl.exe")
	b, err := os.ReadFile(sysCurl)
	if err != nil {
		t.Skipf("没有 curl.exe: %v", err)
	}
	self := filepath.Join(t.TempDir(), "self-curl.exe")
	if err := os.WriteFile(self, b, 0o755); err != nil {
		t.Fatal(err)
	}
	passed := regexp.MustCompile(`^[23]\d\d$`)
	probe := func(exe string, tok windows.Token) string {
		cmd := exec.Command(exe, "-s", "--interface", phys, "-m", "6", "-o", "NUL", "-w", "%{http_code}", "http://1.1.1.1/cdn-cgi/trace")
		if tok != 0 {
			cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(tok)}
		}
		out, _ := cmd.Output()
		return strings.TrimSpace(string(out))
	}
	if code := probe(self, 0); !passed.MatchString(code) {
		t.Skipf("正控制没过:闸没开时绑物理网卡的直连就不通(%q)", code)
	}
	restricted, err := denyOnlyAdminsToken()
	if err != nil {
		t.Fatalf("造受限令牌: %v", err)
	}
	defer restricted.Close()

	if err := Disable(); err != nil {
		t.Fatalf("清理: %v", err)
	}
	defer Disable()
	if _, err := Enable(Spec{LAN: true, Tun4: [4]byte{172, 19, 0, 1}, SelfPath: self}); err != nil {
		t.Fatalf("开闸: %v", err)
	}
	if code := probe(self, 0); !passed.MatchString(code) {
		t.Fatalf("提权的本服务 exe 被拦了(%q):服务自己 / 前台 run 会被自己的闸拦死", code)
	}
	if code := probe(self, restricted); passed.MatchString(code) {
		t.Fatalf("管理员组只能用来拒绝的同一个 exe 也放行了(%q):任何账户起的同路径进程都能直连", code)
	}
	if code := probe(sysCurl, 0); passed.MatchString(code) {
		t.Fatalf("别的 exe 也放行了(%q)", code)
	}
}

// denyOnlyAdminsToken 当前令牌的受限副本:管理员组改成只能用来拒绝、去掉特权 —— 和没提权的管理员、普通用户一样,
// 匹配不上放行本服务的令牌要求。
func denyOnlyAdminsToken() (windows.Token, error) {
	ba, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return 0, err
	}
	var cur windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ASSIGN_PRIMARY, &cur); err != nil {
		return 0, err
	}
	defer cur.Close()
	disable := windows.SIDAndAttributes{Sid: ba}
	var out windows.Token
	const disableMaxPrivilege = 0x1
	r, _, e := windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken").Call(
		uintptr(cur), disableMaxPrivilege, 1, uintptr(unsafe.Pointer(&disable)), 0, 0, 0, 0, uintptr(unsafe.Pointer(&out)))
	if r == 0 {
		return 0, e
	}
	return out, nil
}
