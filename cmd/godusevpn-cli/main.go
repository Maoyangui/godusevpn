// godusevpn-cli 佛跳墙 的命令行(Windows):排障与脚本用,和托盘客户端走同一条控制管道。实现在 internal/cli。
package main

import (
	"os"

	"github.com/Maoyangui/godusevpn/internal/cli"
)

func main() {
	cli.Name, cli.InstallHint = "godusevpn-cli", "服务未运行:先以管理员身份执行 godusevpn-svc install"
	os.Exit(cli.Main(os.Args[1:]))
}
