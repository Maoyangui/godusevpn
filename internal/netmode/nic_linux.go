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
// 改之前把每张网卡的原值存下来(先落盘再动手),断开时按原值写回;
// 守护进程启动时对账一次:上次不是连着关的机(或者设置已关掉这一项)就还原回去,
// 上次是连着关的机就接着关着 —— 它和「全局禁直连」的闸一样是持久的。连接状态读不出来时改看闸还在不在。
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
		if os.IsNotExist(err) {
			// 内核根本没编 IPv6(OpenWrt 精简固件没装 kmod-ipv6、或者 ipv6.disable=1):
			// 没有 v6 协议栈就没有可停用的对象,也就不可能有网卡挂着公网 v6 地址。
			// m28 这里写的就是 "return nil // 内核根本没编 IPv6,没什么可关的";m29 改成硬错误之后,
			// 这类机器在「全局模式 + 全局禁直连」的默认设置下一律连不上。
			setNICWarning("")
			return nil
		}
		return nicResult(tunName, []string{"枚举 IPv6 网卡: " + err.Error()})
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
	if len(saved) == 0 {
		// 读不到状态的那几张(容器的 veth、虚拟机的 tap、拨号口生灭得比我们读它还快)不该把整台机器挡住。
		return nicResult(tunName, errs)
	}
	// 已有备份(上次停了还没还原,比如重建配置重连时守护进程故意不还原)就并进去而不是覆盖:
	// 覆盖的话原来记的那些网卡就丢了,最后还原时开不回来。
	record := map[string]string{}
	if old, err := os.ReadFile(nicBackup()); err == nil {
		if err := json.Unmarshal(old, &record); err != nil || record == nil {
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
	if err := writeNICBackup(record); err != nil {
		return fmt.Errorf("保存 IPv6 备份: %w", err)
	}
	done := map[string]bool{}
	for n := range saved {
		if err := os.WriteFile(filepath.Join(v6ConfDir, n, "disable_ipv6"), []byte("1\n"), 0o644); err != nil {
			if os.IsNotExist(err) {
				continue // 这张网卡在我们读完之后就没了,没什么可关的
			}
			errs = append(errs, n+": "+err.Error())
			continue
		}
		done[n] = true
	}
	// 写完再读一遍确认:写进去不等于生效(只读挂载、LSM 拦截都可能让写入无声失败)。
	for n := range done {
		cur, err := os.ReadFile(filepath.Join(v6ConfDir, n, "disable_ipv6"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || strings.TrimSpace(string(cur)) != "1" {
			if err == nil {
				err = fmt.Errorf("写入后值为 %q", strings.TrimSpace(string(cur)))
			}
			errs = append(errs, n+": 校验失败: "+err.Error())
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
		return nicBackupCorrupt(nicBackup(), why)
	}
	// 备份里的网卡可能已经不在了(拔掉 USB 网卡、关掉虚拟机让 tap/veth 消失、ppp 断开)。
	// 那种"还原不了"其实是"没什么可还原",m29 却把它当成失败,于是备份永远删不掉、
	// 卸载也永远跑不完 —— 一张早就拔掉的网卡把整个产品卡死在机器上。
	// 这里分清楚:没了就从备份里剔掉;只有真失败的才留下来等下次重试。
	var errs []string
	var gone []string
	left := map[string]string{}
	for n, v := range saved {
		p := filepath.Join(v6ConfDir, n, "disable_ipv6")
		if err := os.WriteFile(p, []byte(v+"\n"), 0o644); err != nil {
			if os.IsNotExist(err) {
				gone = append(gone, n)
				continue
			}
			errs = append(errs, n+": "+err.Error())
			left[n] = v
			continue
		}
		cur, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			gone = append(gone, n)
			continue
		}
		if err != nil || strings.TrimSpace(string(cur)) != v {
			if err == nil {
				err = fmt.Errorf("写入后值为 %q", strings.TrimSpace(string(cur)))
			}
			errs = append(errs, n+": "+err.Error())
			left[n] = v
		}
	}
	if len(left) > 0 {
		// 还原不了的留在备份里下次接着试;已经还原好的和已经消失的剔出去,免得同一张网卡
		// 把后面的永远挡住。写不回去也只是下次多试一遍,不改变"还没还原干净"这个结论。
		_ = writeNICBackup(left)
		return fmt.Errorf("还原网卡 IPv6: %s", strings.Join(errs, "; "))
	}
	if err := os.Remove(nicBackup()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除 IPv6 备份: %w", err)
	}
	if len(gone) > 0 {
		setNICWarning("这些网卡已经不在了,当作无需还原: " + strings.Join(gone, ", "))
	} else {
		setNICWarning("")
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
