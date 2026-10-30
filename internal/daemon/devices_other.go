//go:build windows || android || darwin

package daemon

import (
	"github.com/Maoyangui/godusevpn/internal/ipc"
)

// 网关模式与设备发现只在 Linux 软路由上有。
func (d *Daemon) deviceViews() []ipc.DeviceView { return []ipc.DeviceView{} }
func lanInterfaces() []string                   { return nil }
