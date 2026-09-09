//go:build !windows

package ipc

import (
	"context"
	"errors"
	"io/fs"
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
	return filepath.Join(runDir, "godusevpn.sock")
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
	// 只让 root 与同组的人控制:socket 能连上就能改设置、开关连接。
	// 属组按平台定(macOS 要放行 admin 组,否则用户身份的图形界面连不上守护进程)。
	_ = os.Chmod(SocketPath, 0o660)
	if gid := sockGroup(); gid >= 0 {
		_ = os.Chown(SocketPath, -1, gid)
	}
	return ln, nil
}

func dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", SocketPath)
}

func listenerClosed(err error) bool { return errors.Is(err, net.ErrClosed) }

// permissionDenied socket 在但连不上:多半是没用 root 跑命令行。
func permissionDenied(err error) bool { return errors.Is(err, fs.ErrPermission) }
