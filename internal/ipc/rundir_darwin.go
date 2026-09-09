package ipc

import (
	"os/user"
	"strconv"
)

// macOS 没有 /run,运行时目录是 /var/run(指向 /private/var/run)。
const runDir = "/var/run"

// sockGroup macOS 上把控制口的属组改成 admin。
//
// 守护进程是 root 跑的(launchd),图形界面是当前用户跑的:socket 若维持 root:wheel 0660,
// 界面根本连不上,整个程序就只剩一个"服务未运行"。Windows 上是靠命名管道的 ACL 放行普通用户,
// 这里对应的做法是放行 admin 组 —— Mac 的主用户(能 sudo 的那个)都在这个组里,
// 标准用户仍然连不上,不至于谁都能开关 VPN、改路由。
func sockGroup() int {
	if g, err := user.LookupGroup("admin"); err == nil {
		if gid, err := strconv.Atoi(g.Gid); err == nil {
			return gid
		}
	}
	return 80 // macOS 上 admin 组固定是 80,查不到就按这个来
}
