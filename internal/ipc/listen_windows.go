package ipc

import (
	"context"
	"errors"
	"net"

	"github.com/Microsoft/go-winio"
)

// PipeName 管道名。ACL:SYSTEM 与管理员完全控制,本机已登录用户可读写。
const PipeName = `\\.\pipe\godusevpn`

// Address 控制口地址(诊断信息里显示用)。
func Address() string { return PipeName }

// sddl:SYSTEM 与管理员完全控制,已登录的本机用户可读写(能控制连接、改设置);其他人连不上。
const sddl = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;AU)"

func listen() (net.Listener, error) {
	return winio.ListenPipe(PipeName, &winio.PipeConfig{SecurityDescriptor: sddl})
}

func dial(ctx context.Context) (net.Conn, error) {
	return winio.DialPipeContext(ctx, PipeName)
}

func listenerClosed(err error) bool { return errors.Is(err, winio.ErrPipeListenerClosed) }

// 管道的 ACL 已放开给本机登录用户,连不上就是服务没跑。
func permissionDenied(error) bool { return false }
