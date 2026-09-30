//go:build linux && !android

package netmode

import (
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/Maoyangui/godusevpn/internal/builder"
)

// SelfControl 给守护进程自己直连出去的套接字(订阅刷新的回退、DoH)打上 builder.RouteMark,
// 全局禁直连的闸按这个标记放行本服务(见 guard_linux.go),和内核出站带的 route.default_mark 是同一个。
// 打不上(不是 root)就算了:没标记的包只会被闸拦下,不会漏;闸不在时照常连得上。
func SelfControl(_, _ string, c syscall.RawConn) error {
	return c.Control(func(fd uintptr) {
		_ = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, builder.RouteMark)
	})
}
