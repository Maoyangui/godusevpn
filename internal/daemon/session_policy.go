package daemon

import (
	"time"

	"github.com/Maoyangui/godusevpn/internal/ipc"
)

// 会话看护的策略(core.SessionPolicy):内核里 hysteria2 / tuic 出站的会话被判废或被"网络变化"拆掉时,
// 内核那层回到这里问"用户还想连着吗""这个节点正在用吗",并把每一次重建记下来。
//
// 全部动作都在隧道内部:重建只是对同一个节点重新握手,不换节点、不开任何直连、不碰闸和网卡。
// 换节点仍然只有两条路 —— 用户手动选,或者状态机走到"临时换线"(那是用户定下的自救逻辑)。
// 这里对状态机的唯一影响是:会话被判废时立刻敲一次健康检查,别再等三分钟的定时器。

// Wanted 用户想连着且内核在跑。
func (d *Daemon) Wanted() bool { return d.machine.Wanted() && d.core.Running() }

// InUse 这个出站是不是此刻正在用的节点。
func (d *Daemon) InUse(tag string) bool { return tag != "" && d.currentNode() == tag }

// OnSick 当前节点的会话被判定"活着却不投递"。记数,并让状态机马上做一次健康检查。
func (d *Daemon) OnSick(tag, reason string) {
	d.mu.Lock()
	d.tunnel.Sick++
	d.mu.Unlock()
	d.machine.CheckNow()
}

// OnRebuild 会话被拆掉、开始重建。
func (d *Daemon) OnRebuild(tag, reason string) {
	d.mu.Lock()
	d.tunnel.Rebuilds++
	d.tunnel.LastAt, d.tunnel.LastNode, d.tunnel.LastReason = time.Now().Unix(), tag, reason
	d.mu.Unlock()
	d.logf("节点「%s」的会话已拆掉(%s),正在重新握手", tag, reason)
}

// tunnelView 看护记录的快照;一次都没发生过就不给,界面上不显示这一栏。
func (d *Daemon) tunnelView() *ipc.TunnelView {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.tunnel.Rebuilds == 0 && d.tunnel.Sick == 0 {
		return nil
	}
	v := d.tunnel
	return &v
}
