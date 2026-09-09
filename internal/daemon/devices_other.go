//go:build windows || android || darwin

package daemon

import (
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 网关模式与设备发现只在 Linux 软路由上有。
func (d *Daemon) deviceViews() []ipc.DeviceView       { return []ipc.DeviceView{} }
func (d *Daemon) resolveDeviceIPs(*settings.Settings) {}
func (d *Daemon) deviceRescan()                       {}
func lanInterfaces() []string                         { return nil }
