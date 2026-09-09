//go:build !windows && !android && !darwin

package netmode

import "os/exec"

// 网关模式下的防火墙:sing-box 的 auto_redirect 自己用 nftables 把经本机转发的 TCP / UDP 收进来,
// 连局域网设备发给路由器 53 端口的 DNS 查询也由它 DNAT 到 TUN 地址(table inet sing-box 的 prerouting 链),
// 我们不需要再加规则;早期版本自己建过一张 inet godusevpn 表做 DNS 劫持,反而抢在它前面把查询改坏了,这里只负责清掉残留。

const nftTable = "godusevpn"

// ApplyGateway 目前只清理旧版本留下的表;参数保留给以后的 TProxy 模式。
func ApplyGateway(string, []string, bool) error {
	ClearGateway()
	return nil
}

// ClearGateway 删掉本程序的 nft 表(不存在也无妨)。
func ClearGateway() {
	_ = exec.Command("nft", "delete", "table", "inet", nftTable).Run()
}
