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
// 改之前记下每个服务原来的模式(先落盘再动手),断开时还原;
// 守护进程启动时对账一次:上次不是连着关的机(或者设置已关掉这一项)就还原回去,
// 上次是连着关的机就接着关着 —— 它和「全局禁直连」的闸一样是持久的。连接状态读不出来时改看闸还在不在。
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

func DisableNICIPv6(tunName string) error {
	svcs, err := networkServices()
	if err != nil {
		return nicResult(tunName, []string{"列网络服务: " + err.Error()})
	}
	saved := map[string]string{}
	var errs []string
	for _, s := range svcs {
		switch v6Mode(s) {
		case "Automatic":
			saved[s] = "Automatic"
		case "Off": // 用户原本的设置不碰
		case "Manual":
			// 本文件开头写的就是"设成 Manual(手工填了地址)的一律不碰 —— 那种情况还原不回原样,宁可不动"。
			// m29 把它改成了硬错误,于是只要有一个网络服务的「配置 IPv6」是"手动"(企业/实验室的静态 v6、
			// 手工配过的 Thunderbolt Bridge),整台 Mac 在默认设置下就连不上 —— 和文件自己的说明直接打架。
			// 跳过它;这张卡上真挂着公网 v6 的话,nicResult 里的泄漏复核会把它揪出来。
			errs = append(errs, s+": IPv6 是手工配置的,不动它(动了还原不回原样)")
		default:
			// networksetup -getinfo 失败或者输出里根本没有 "IPv6:" 这一行:被停用的服务(名字前带 *)、
			// VPN 类服务、没有硬件的 Thunderbolt Bridge / iPhone USB 都会这样。跳过,别挡住连接。
			errs = append(errs, s+": 读不到 IPv6 配置")
		}
	}
	if len(saved) == 0 {
		return nicResult(tunName, errs)
	}
	// 已有备份(上次停了还没还原,比如重建配置重连时守护进程故意不还原)就并进去而不是覆盖:
	// 覆盖的话原来记的那些网卡就丢了,最后还原时开不回来。
	record := map[string]string{}
	if old, err := os.ReadFile(nicBackup()); err == nil {
		if err := json.Unmarshal(old, &record); err != nil || record == nil {
			// 不能拿当前状态重建:当前"已经关掉"的那些分不清是用户自己关的还是我们关的,
			// 重建等于把它们永久留在关闭状态。挪走、说清楚、这一轮不动网卡,下一轮就是干净的一次。
			why := "内容为空"
			if err != nil {
				why = err.Error()
			}
			return nicBackupCorrupt(nicBackup(), why)
		}
	} else if !os.IsNotExist(err) {
		return nicResult(tunName, append(errs, "读取 IPv6 备份: "+err.Error()))
	}
	for n, v := range saved {
		if _, dup := record[n]; !dup {
			record[n] = v
		}
	}
	// 备份必须先落盘再动手:存不下来就绝对不能动网卡,否则断开时无从还原。这一条是硬的。
	if err := writeNICBackup(record); err != nil {
		return fmt.Errorf("保存 IPv6 备份: %w", err)
	}
	for s := range saved {
		if out, err := exec.Command("networksetup", "-setv6off", s).CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s", s, strings.TrimSpace(string(out))))
		}
	}
	return nicResult(tunName, errs)
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
	if err := json.Unmarshal(b, &saved); err != nil || saved == nil {
		why := "内容为空"
		if err != nil {
			why = err.Error()
		}
		return nicRestoreCorrupt(nicBackup(), why)
	}
	// 备份里的网络服务可能已经被删掉或改名了。那种"还原不了"其实是"没什么可还原",
	// m29 却把它当成失败,于是备份永远删不掉、卸载也永远跑不完。
	// 这里分清楚:服务已经不在了就从备份里剔掉;只有真失败的才留下来等下次重试。
	live := map[string]bool{}
	if svcs, err := networkServices(); err == nil {
		for _, s := range svcs {
			live[s] = true
		}
	} else {
		for s := range saved {
			live[s] = true // 列不出服务就别乱剔,当成都还在,下次再说
		}
	}
	var errs []string
	var gone []string
	left := map[string]string{}
	for s, mode := range saved {
		if mode != "Automatic" {
			continue
		}
		if !live[s] {
			gone = append(gone, s)
			continue
		}
		if out, err := exec.Command("networksetup", "-setv6automatic", s).CombinedOutput(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s", s, strings.TrimSpace(string(out))))
			left[s] = mode
			continue
		}
		if got := v6Mode(s); got != "Automatic" {
			errs = append(errs, fmt.Sprintf("%s: 还原后状态为 %q", s, got))
			left[s] = mode
		}
	}
	if len(left) > 0 {
		_ = writeNICBackup(left)
		return fmt.Errorf("还原网卡 IPv6: %s", strings.Join(errs, "; "))
	}
	if err := os.Remove(nicBackup()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除 IPv6 备份: %w", err)
	}
	if len(gone) > 0 {
		setNICWarning("这些网络服务已经不在了,当作无需还原: " + strings.Join(gone, ", "))
	} else {
		setNICWarning("")
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
