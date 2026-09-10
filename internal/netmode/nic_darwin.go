package netmode

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Maoyangui/godusevpn/internal/paths"
)

// macOS 上停用网卡 IPv6 用 networksetup -setv6off,按"网络服务"(Wi-Fi、Ethernet…)逐个来,
// 等同于在 系统设置 → 网络 → 详细信息 → TCP/IP 里把"配置 IPv6"选成"关闭"。
// 为什么要做见 nic_windows.go 里的说明 —— 挡数据包挡不住"程序读走网卡上的公网 v6 地址"。
//
// 改之前记下每个服务原来的模式(先落盘再动手),断开时还原;守护进程启动时也无条件还原一次。
// 本来就是 Off 的不记也不动;设成 Manual(手工填了地址)的一律不碰 —— 那种情况还原不回原样,宁可不动。

func nicBackup() string { return filepath.Join(paths.DataDir(), "nic-ipv6-backup.json") }

func DisableNICIPv6(string) error {
	svcs, err := networkServices()
	if err != nil {
		return err
	}
	saved := map[string]string{}
	for _, s := range svcs {
		switch v6Mode(s) {
		case "Automatic":
			saved[s] = "Automatic"
		default: // Off:已经关了;Manual:手工配置,不碰;取不到:跳过
		}
	}
	if len(saved) == 0 {
		return nil
	}
	if b, err := json.Marshal(saved); err == nil {
		_ = os.WriteFile(nicBackup(), b, 0o600)
	}
	var errs []string
	for s := range saved {
		if out, err := exec.Command("networksetup", "-setv6off", s).CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s", s, strings.TrimSpace(string(out))))
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
		for s, mode := range saved {
			if mode == "Automatic" {
				_ = exec.Command("networksetup", "-setv6automatic", s).Run()
			}
		}
	}
	_ = os.Remove(nicBackup())
}

// v6Mode 某个网络服务的 IPv6 配置方式:Automatic / Off / Manual;取不到返回空串。
func v6Mode(svc string) string {
	out, err := exec.Command("networksetup", "-getinfo", svc).Output()
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(ln), "IPv6:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
