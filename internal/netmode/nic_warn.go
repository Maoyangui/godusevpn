package netmode

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Maoyangui/godusevpn/internal/paths"
)

// osRename 抽一层只为让 nicBackupCorrupt 在测试里不碰真实文件系统。
var osRename = os.Rename

// 这一层解决的是同一个问题:动网卡 IPv6 的时候,总有些对象是我们够不着的 ——
// 内核根本没编 IPv6、网卡在脚本跑到一半被拔掉、某个网络服务的 IPv6 是用户手工配的、
// 容器的 veth 生灭得比我们读它还快。
//
// m29 把这些一律改成了硬错误,而 daemon.prepare 里紧挨着 syncGuard 的那次 syncNICIPv6 又是连接前置,
// 于是"有一张网卡没动成"= 整台机器连不上。用户唯一的绕法是去**关掉**「全局禁直连」——
// 一道以隐私为名的检查,实际把人推向了更不私密的配置。m28 则是另一个极端:全部吞掉,
// 真漏了也不说。
//
// 这里取中间:**只有确证此刻真有网卡挂着公网 IPv6 地址,才算失败。** 那种情况是可满足的 ——
// 用户能自己去关掉那张网卡的 v6;查下来没有暴露、或者查不出来,就照常连着并如实告警。

var (
	nicWarnMu sync.Mutex
	nicWarn   string
	routeWarn string
)

func setNICWarning(s string) {
	nicWarnMu.Lock()
	nicWarn = s
	nicWarnMu.Unlock()
}

// NICWarning 上次动网卡 IPv6 时没做成的那部分;空 = 全做到了。和 GuardWarning 一个路数:
// 做不到的事如实报出来,而不是把连接挡死。
func NICWarning() string {
	nicWarnMu.Lock()
	defer nicWarnMu.Unlock()
	return nicWarn
}

// setRouteWarning / RouteWarning 是另一个槽:Linux 的"回包走主表"策略路由和网卡 IPv6 是两件事,
// 共用一个格子的话后写的会把先写的盖掉,用户只看得见其中一条。
func setRouteWarning(s string) {
	nicWarnMu.Lock()
	routeWarn = s
	nicWarnMu.Unlock()
}

// RouteWarning 上次清理 / 添加回包路由规则时没做成的那部分;空 = 没问题。
func RouteWarning() string {
	nicWarnMu.Lock()
	defer nicWarnMu.Unlock()
	return routeWarn
}

// nicBackupCorrupt 备份文件坏了(0 字节、被截断、手工改过)时统一这么处理:挪到 .bad 留证,
// 如实告警,然后**返回 nil**。
//
// 为什么不报错:报错的唯一后果是把人卡死。停用那一路报错 = 连不上(而且关掉「连接时停用网卡 IPv6」
// 也救不回来,因为那条开关只影响"要不要关",不影响"要不要先读备份");还原那一路报错 = 备份永远删不掉、
// 卸载永远跑不完。而原值本来就已经随文件一起丢了,继续硬失败一分钱也换不回来。
// 代价是那几张网卡的原始状态确实丢了,所以这里把话说清楚,让用户知道要手动开回去。
func nicBackupCorrupt(path, why string) error {
	_ = osRename(path, path+".bad")
	msg := "网卡 IPv6 备份文件已损坏(" + why + "),已挪到 " + path + ".bad。" +
		"里面记的原始状态没了 —— 如果某些网卡的 IPv6 现在是关着的,需要手动开回去。"
	setNICWarning(msg)
	recordNICLoss(msg)
	return nil
}

// ---- 网卡原值丢失的持久记录 ----
//
// 只记在内存里(0.7.4)的话,好几条路都会把这件事吞掉:停用那一路先把坏行剔掉、重新记成"本来就是关的",
// 之后还原报全成功;服务在跑时「恢复网络」先让守护进程还原、自己再还原时备份已经没了;点断开、改设置时
// 守护进程只写一行日志,界面根本不知道。所以落盘:谁发现谁记,界面一直显示到用户点"知道了",
// 「恢复网络」弹窗和卸载说出来之后删掉。

func nicLossPath() string { return filepath.Join(paths.DataDir(), "nic-ipv6-lost.txt") }

// recordNICLoss 追加一条记录。写不进去就算了(同一句话还在 NICWarning 与日志里)。
func recordNICLoss(msg string) {
	f, err := os.OpenFile(nicLossPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(time.Now().Format("2006-01-02 15:04") + " " + msg + "\n")
}

// NICLossNote 还没被用户确认的记录;没有就是空串。
func NICLossNote() string {
	b, err := os.ReadFile(nicLossPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ClearNICLoss 用户已经看到了:删掉记录。
func ClearNICLoss() error {
	if err := os.Remove(nicLossPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// NICRestoreIncomplete 还原做完了能做的(备份已清掉,不必再重试),但备份里有几行 / 整份坏了,
// 那几张网卡动手前的状态丢了,可能还关着 IPv6。不算失败,但调用方必须把 Detail 说给用户:
// 0.7.4 只把它记进内存里的告警,「恢复网络」弹窗和卸载都读不到,照样说"已还原"。
type NICRestoreIncomplete struct{ Detail string }

func (e *NICRestoreIncomplete) Error() string { return e.Detail }

// nicRestoreCorrupt 还原路径上备份整份坏了:挪到 .bad 留证、记下、返回 NICRestoreIncomplete。
// 挪不走(只读分区之类)就不能说"已挪走、不必重试":返回普通错误,备份留着,下次再试。
func nicRestoreCorrupt(path, why string) error {
	if err := osRename(path, path+".bad"); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("网卡 IPv6 备份文件已损坏(%s),而且挪不走(%v),留着下次再试", why, err)
	}
	msg := "网卡 IPv6 备份文件已损坏(" + why + "),已挪到 " + path + ".bad。" +
		"里面记的原始状态没了 —— 如果某些网卡的 IPv6 现在是关着的,需要手动开回去。"
	setNICWarning(msg)
	recordNICLoss(msg)
	return &NICRestoreIncomplete{Detail: msg}
}

// nicLeakConfirmed 抽成变量只为可测:真机上就是 NICIPv6LeakConfirmed。
var nicLeakConfirmed = NICIPv6LeakConfirmed

// nicResult 各平台 DisableNICIPv6 的统一收尾:errs 是"没动成的那些网卡"。
func nicResult(tunName string, errs []string) error {
	if len(errs) == 0 {
		setNICWarning("")
		return nil
	}
	detail := strings.Join(errs, "; ")
	if nicLeakConfirmed(tunName) {
		setNICWarning("")
		return fmt.Errorf("停用网卡 IPv6 没做完,而且确有网卡还挂着公网 IPv6 地址: %s", detail)
	}
	setNICWarning("这些网卡的 IPv6 没能停用(此刻查下来没有哪张网卡暴露公网 v6 地址,先照常连着): " + detail)
	return nil
}
