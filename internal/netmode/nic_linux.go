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

const v6ConfDir = "/proc/sys/net/ipv6/conf"

func DisableNICIPv6(tunName string) error {
	names, err := os.ReadDir(v6ConfDir)
	if err != nil {
		return fmt.Errorf("枚举 IPv6 网卡: %w", err)
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
			errs = append(errs, n+": "+err.Error())
			continue
		}
		if strings.TrimSpace(string(cur)) == "1" {
			continue // 本来就关着,别记也别动
		}
		if strings.TrimSpace(string(cur)) != "0" {
			errs = append(errs, n+": 无法识别 disable_ipv6 值")
			continue
		}
		saved[n] = strings.TrimSpace(string(cur))
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
	for n := range saved {
		if err := os.WriteFile(filepath.Join(v6ConfDir, n, "disable_ipv6"), []byte("1\n"), 0o644); err != nil {
			errs = append(errs, n+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("停用网卡 IPv6: %s", strings.Join(errs, "; "))
	}
	for n := range saved {
		cur, err := os.ReadFile(filepath.Join(v6ConfDir, n, "disable_ipv6"))
		if err != nil || strings.TrimSpace(string(cur)) != "1" {
			if err == nil {
				err = fmt.Errorf("写入后值为 %q", strings.TrimSpace(string(cur)))
			}
			return fmt.Errorf("校验网卡 %s IPv6 状态: %w", n, err)
		}
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
	for n, v := range saved {
		p := filepath.Join(v6ConfDir, n, "disable_ipv6")
		if err := os.WriteFile(p, []byte(v+"\n"), 0o644); err != nil {
			errs = append(errs, n+": "+err.Error())
			continue
		}
		cur, err := os.ReadFile(p)
		if err != nil || strings.TrimSpace(string(cur)) != v {
			if err == nil {
				err = fmt.Errorf("写入后值为 %q", strings.TrimSpace(string(cur)))
			}
			errs = append(errs, n+": "+err.Error())
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

// NICIPv6Off 网卡的 IPv6 此刻是不是被我们关着的(有备份 = 关过还没还原)。
func NICIPv6Off() bool {
	_, err := os.Stat(nicBackup())
	return err == nil
}

// NICIPv6Manageable 这台机器能不能动物理网卡的 IPv6。
func NICIPv6Manageable() bool { return true }
