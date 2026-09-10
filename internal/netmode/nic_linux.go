//go:build !android

package netmode

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Maoyangui/godusevpn/internal/paths"
)

// Linux 上停用网卡 IPv6 靠 sysctl:net.ipv6.conf.<网卡>.disable_ipv6 置 1,内核会把这张网卡上的
// v6 地址一并清掉。为什么要做见 nic_windows.go 里的说明 —— 挡数据包挡不住"程序读走网卡上的公网 v6 地址"。
//
// 不设例外:软路由的网关模式下同样会关(那正是希望整个局域网都没有 v6 出口的场景),
// 只有隧道自己那张网卡不动 —— 它要靠 v6 地址把 v6 接进来再拒绝。
// 改之前把每张网卡的原值存下来(先落盘再动手),断开时按原值写回;守护进程启动时也无条件还原一次。
// 本来就已经是 1 的网卡不记也不动 —— 那是用户自己关的,断开后仍旧保持关闭。

func nicBackup() string { return filepath.Join(paths.DataDir(), "nic-ipv6-backup.json") }

const v6ConfDir = "/proc/sys/net/ipv6/conf"

func DisableNICIPv6(tunName string) error {
	names, err := os.ReadDir(v6ConfDir)
	if err != nil {
		return nil // 内核根本没编 IPv6,没什么可关的
	}
	saved := map[string]string{}
	var errs []string
	for _, e := range names {
		n := e.Name()
		// all / default 是模板,改了会影响之后新建的网卡(包括我们自己的隧道);lo 关了会踩到本机服务
		if n == "all" || n == "default" || n == "lo" || n == tunName {
			continue
		}
		p := filepath.Join(v6ConfDir, n, "disable_ipv6")
		cur, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(cur)) == "1" {
			continue // 本来就关着,别记也别动
		}
		saved[n] = strings.TrimSpace(string(cur))
	}
	if len(saved) == 0 {
		return nil
	}
	if b, err := json.Marshal(saved); err == nil {
		_ = os.WriteFile(nicBackup(), b, 0o600)
	}
	for n := range saved {
		if err := os.WriteFile(filepath.Join(v6ConfDir, n, "disable_ipv6"), []byte("1\n"), 0o644); err != nil {
			errs = append(errs, n+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("停用网卡 IPv6: %s", strings.Join(errs, "; "))
	}
	return nil
}

func RestoreNICIPv6() {
	b, err := os.ReadFile(nicBackup())
	if err != nil {
		return
	}
	var saved map[string]string
	if json.Unmarshal(b, &saved) == nil {
		for n, v := range saved {
			_ = os.WriteFile(filepath.Join(v6ConfDir, n, "disable_ipv6"), []byte(v+"\n"), 0o644)
		}
	}
	_ = os.Remove(nicBackup())
}
