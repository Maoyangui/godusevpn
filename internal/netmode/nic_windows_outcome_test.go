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
	t.Run("只有消失的网卡:成功但有告警", func(t *testing.T) {
		w, err := restoreOutcome("GODUSEVPN-GONE: USB 网卡")
		if err != nil || !strings.Contains(w, "USB 网卡") {
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
