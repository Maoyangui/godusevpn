//go:build !windows

package ipc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
)

// SocketPath 控制口 Unix socket 路径;可用环境变量 GODUSEVPN_SOCK 覆盖(测试与非 root 运行用)。
var SocketPath = defaultSocketPath()

func defaultSocketPath() string {
	if p := os.Getenv("GODUSEVPN_SOCK"); p != "" {
		return p
	}
	return "/run/godusevpn.sock"
}

// Address 控制口地址(诊断信息里显示用)。
func Address() string { return SocketPath }

func listen() (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(SocketPath), 0o755); err != nil {
		return nil, err
	}
	_ = os.Remove(SocketPath) // 上次没收干净的残留
	ln, err := net.Listen("unix", SocketPath)
	if err != nil {
		return nil, err
	}
	// 只让 root(及同组)控制:socket 能连上就能改设置、开关连接
	_ = os.Chmod(SocketPath, 0o660)
	return ln, nil
}

func dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", SocketPath)
}

func listenerClosed(err error) bool { return errors.Is(err, net.ErrClosed) }
