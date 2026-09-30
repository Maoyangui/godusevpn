//go:build windows

package netmode

import (
	"errors"
	"strings"
	"testing"
)

// 还原脚本输出 → 结果。0.7.4 把"备份里有无效行"只记成内存告警、返回 nil,于是「恢复网络」弹窗说
// "网卡 IPv6 也还原了",而那张网卡的 v6 其实还关着。这里钉住各种输出组合的结果。
func TestRestoreOutcome(t *testing.T) {
	t.Run("什么都没有:成功", func(t *testing.T) {
		if w, err := restoreOutcome(""); err != nil || w != "" {
			t.Fatalf("got %q %v", w, err)
		}
	})
	// 还原时不在的网卡挪进待还原清单:做完了能做的,但必须如实说(0.7.5 只进内存告警,日志与「恢复网络」弹窗说"已还原")
	t.Run("有挪进待还原清单的网卡:做完了但要如实说", func(t *testing.T) {
		w, err := restoreOutcome("GODUSEVPN-PENDING: WLAN 2, 以太网 2")
		var inc *NICRestoreIncomplete
		if !errors.As(err, &inc) || !strings.Contains(inc.Detail, "WLAN 2, 以太网 2") || !strings.Contains(w, "不在") {
			t.Fatalf("got %q %v", w, err)
		}
	})
	t.Run("有 FAILED 也有待还原:普通失败,待还原只在告警里", func(t *testing.T) {
		w, err := restoreOutcome("GODUSEVPN-FAILED: 以太网: 拒绝访问\nGODUSEVPN-PENDING: WLAN 2")
		var inc *NICRestoreIncomplete
		if err == nil || errors.As(err, &inc) || strings.Contains(err.Error(), "WLAN 2") || !strings.Contains(w, "WLAN 2") {
			t.Fatalf("got %q %v", w, err)
		}
	})
	t.Run("只有无效行:做完了但要如实说", func(t *testing.T) {
		w, err := restoreOutcome("GODUSEVPN-CORRUPT: 2")
		var inc *NICRestoreIncomplete
		if !errors.As(err, &inc) || !strings.Contains(inc.Detail, "2 行无效") || !strings.Contains(w, "2 行无效") {
			t.Fatalf("got %q %v", w, err)
		}
	})
	t.Run("无效行挪不进 .bad:也要说,而且说清楚没保住", func(t *testing.T) {
		_, err := restoreOutcome("GODUSEVPN-CORRUPT-LOST: 1")
		var inc *NICRestoreIncomplete
		if !errors.As(err, &inc) || !strings.Contains(inc.Detail, "没保住") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("有 FAILED:普通失败;坏行说明只在告警里,不重复进错误", func(t *testing.T) {
		w, err := restoreOutcome("GODUSEVPN-CORRUPT: 1\nGODUSEVPN-FAILED: 以太网: 拒绝访问")
		var inc *NICRestoreIncomplete
		if err == nil || errors.As(err, &inc) || !strings.Contains(err.Error(), "拒绝访问") || strings.Contains(err.Error(), "行无效") || !strings.Contains(w, "1 行无效") {
			t.Fatalf("got %q %v", w, err)
		}
	})
}

// 网卡名按 -ceq 精确比对后经管道交给 Enable-/Disable-NetAdapterBinding。-Name 在这组命令里是通配符(WQL LIKE):
// 「本地连接* 2」会连带开 / 关「本地连接 2」(用户自己关的那张),名字带 [ ] 的又匹配不到自己、被当成"已不在"。
// 还原在 -IncludeHidden 的列表里找(拔掉 / 隐藏的网卡也试),找不到的挪进待还原清单而不是丢掉;
// 停用时清单里的网卡再出现,原值按清单记成开着。只看生成的脚本文本,不跑任何命令。
func TestNICScriptsMatchAdapterNamesExactly(t *testing.T) {
	for name, s := range map[string]string{"停用": disableScript("godusevpn"), "还原": restoreScript()} {
		for _, bad := range []string{"-Name $a.Name", "-Name $p[0]", "-Name $w.Name"} {
			if strings.Contains(s, bad) {
				t.Fatalf("%s脚本还在用 %s:-Name 是通配符,会误中别的网卡或找不到自己", name, bad)
			}
		}
		if !strings.Contains(s, "-ceq") || !strings.Contains(s, nicPending()) {
			t.Fatalf("%s脚本没按名字精确比对,或没管待还原清单", name)
		}
	}
	r := restoreScript()
	for _, want := range []string{"Get-NetAdapterBinding -ComponentID ms_tcpip6 -IncludeHidden", "| Enable-NetAdapterBinding", "GODUSEVPN-PENDING: ", "Not Present"} {
		if !strings.Contains(r, want) {
			t.Fatalf("还原脚本里没有 %q", want)
		}
	}
	if strings.Index(r, "Write-Lines $pf") > strings.Index(r, "Write-Lines $f $left") {
		t.Fatal("还原脚本要先落待还原清单、再动备份:反过来的话中间被杀掉,挪进清单的那几行就没了")
	}
	d := disableScript("godusevpn")
	if !strings.Contains(d, "| Disable-NetAdapterBinding") || !strings.Contains(d, "pend.ContainsKey($_.Name)") {
		t.Fatal("停用脚本没经管道停用、或没按待还原清单把再出现的网卡记成开着")
	}
	if strings.Index(d, "Write-Lines $b") > strings.Index(d, "Write-Lines $pf $rest") {
		t.Fatal("停用脚本要先把原值写进备份、再从待还原清单里拿掉")
	}
}
