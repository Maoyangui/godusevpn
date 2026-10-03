package daemon

import (
	"strings"
	"testing"
)

// Linux 上"有备份"不等于网卡 IPv6 还关着:停用不过重启,networkd / NetworkManager 配置网卡时还会改回去
// (真机重启实测:开机闸关上,networkd 起来又打开,守护进程以为"一直关着"就不管了)。
// 启动对账、连接时同步、每 5 秒的巡检都要看 NICIPv6Drifted,被改回去就重新停用;对完账撤掉开机挡路由器通告的表。
func TestNICDriftIsRechecked(t *testing.T) {
	src := readDaemonSource(t)
	rec := funcBody(t, src, "func (d *Daemon) reconcileNICIPv6() {")
	if !strings.Contains(rec, "!netmode.NICIPv6Leaking(builder.TunName) && !netmode.NICIPv6Drifted()") {
		t.Fatal("启动对账:记着关过还要看有没有被改回去,不能直接当成一直关着")
	}
	if !strings.Contains(rec, "netmode.DropBootRA()") || !strings.Contains(rec, "!d.nicDisablePending.Load()") {
		t.Fatal("对完账(停用没失败)要撤掉开机挡路由器通告的表")
	}
	sync := funcBody(t, src, "func (d *Daemon) syncNICIPv6() error {")
	if strings.Count(sync, "netmode.NICIPv6Drifted()") < 2 {
		t.Fatal("连接时同步:两处\"备份在就当关着\"都要看有没有被改回去")
	}
	if !strings.Contains(sync, "netmode.DropBootRA()") || !strings.Contains(sync, "!d.nicDisablePending.Load()") {
		t.Fatal("连接时同步:开机没停成、后来在这里停成或还原了,也要撤掉挡路由器通告的表,不然还原后拿不到 v6 地址")
	}
	loop := funcBody(t, src, "func (d *Daemon) nicIPv6Loop(ctx context.Context) {")
	if !strings.Contains(loop, "netmode.NICIPv6Drifted()") || !strings.Contains(loop, "netmode.DropBootRA()") {
		t.Fatal("巡检:被改回去也要重新停用,停用成功后放开开机挡着的路由器通告")
	}
}
