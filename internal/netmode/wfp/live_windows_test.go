//go:build windows

package wfp

import (
	"os"
	"os/exec"
	"strings"
	"testing"

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
	// 按固定 GUID 查(按名字查会撞上正在跑的旧版服务那个动态会话的提供者)
	out, _ = exec.Command("netsh", "wfp", "show", "state", "file=-").Output()
	if strings.Contains(strings.ToLower(string(out)), "6f6d9e2c-3a41-4b8e-9d55-676f64757365") {
		t.Fatal("撤闸后提供者还在")
	}
	if strings.Contains(strings.ToLower(string(out)), "6f6d9e2d-3a41-4b8e-9d55-676f64757365") {
		t.Fatal("撤闸后子层还在")
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
