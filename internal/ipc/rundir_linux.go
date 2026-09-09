package ipc

// Linux(含 Android / OpenWrt / Entware):运行时目录在 /run。
const runDir = "/run"

// sockGroup Linux 上不改控制口的属组:界面是带密码的网页面板,不经这个 socket,
// 命令行则本来就要 root。返回 -1 表示保持原样。
func sockGroup() int { return -1 }
