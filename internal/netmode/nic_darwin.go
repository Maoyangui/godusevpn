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

func writeNICBackup(record map[string]string) error {
	b, err := json.Marshal(record)
	if err != nil {
		return err
	}
	tmp := nicBackup() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, nicBackup()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func DisableNICIPv6(string) error {
	svcs, err := networkServices()
	if err != nil {
		return err
	}
	saved := map[string]string{}
	var errs []string
	for _, s := range svcs {
		switch v6Mode(s) {
		case "Automatic":
			saved[s] = "Automatic"
		case "Off": // 用户原本的设置不碰
		case "Manual":
			errs = append(errs, s+": IPv6 为手工配置,无法安全停用")
		default:
			errs = append(errs, s+": 无法读取 IPv6 配置")
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("读取网卡 IPv6 状态: %s", strings.Join(errs, "; "))
	}
	if len(saved) == 0 {
		return nil
	}
	// 已有备份(上次停了还没还原,比如重建配置重连时守护进程故意不还原)就并进去而不是覆盖:
	// 覆盖的话原来记的那些网卡就丢了,最后还原时开不回来。
	record := map[string]string{}
	if old, err := os.ReadFile(nicBackup()); err == nil {
		if err := json.Unmarshal(old, &record); err != nil {
			return fmt.Errorf("读取 IPv6 备份: %w", err)
		}
		if record == nil {
			return fmt.Errorf("读取 IPv6 备份: 内容为空")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("读取 IPv6 备份: %w", err)
	}
	for n, v := range saved {
		if _, dup := record[n]; !dup {
			record[n] = v
		}
	}
	if err := writeNICBackup(record); err != nil {
		return fmt.Errorf("保存 IPv6 备份: %w", err)
	}
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

func RestoreNICIPv6() error {
	b, err := os.ReadFile(nicBackup())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var saved map[string]string
	if err := json.Unmarshal(b, &saved); err != nil {
		return fmt.Errorf("解析 IPv6 备份: %w", err)
	}
	if saved == nil {
		return fmt.Errorf("解析 IPv6 备份: 内容为空")
	}
	var errs []string
	for s, mode := range saved {
		if mode != "Automatic" {
			continue
		}
		if out, err := exec.Command("networksetup", "-setv6automatic", s).CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s", s, strings.TrimSpace(string(out))))
			continue
		}
		if got := v6Mode(s); got != "Automatic" {
			errs = append(errs, fmt.Sprintf("%s: 还原后状态为 %q", s, got))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("还原网卡 IPv6: %s", strings.Join(errs, "; "))
	}
	if err := os.Remove(nicBackup()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除 IPv6 备份: %w", err)
	}
	return nil
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

// NICIPv6Off 网卡的 IPv6 此刻是不是被我们关着的(有备份 = 关过还没还原)。
func NICIPv6Off() bool {
	_, err := os.Stat(nicBackup())
	return err == nil
}

// NICIPv6Manageable 这台机器能不能动物理网卡的 IPv6。
func NICIPv6Manageable() bool { return true }
