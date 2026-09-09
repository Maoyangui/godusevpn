// Windows 上跟系统打交道的那几件事:提权、拉安装包、在资源管理器里定位文件。
package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/wailsapp/wails/v2/pkg/options"
	woptions "github.com/wailsapp/wails/v2/pkg/options/windows"
)

// tuneOptions 补上 Windows 独有的窗口选项。
func tuneOptions(o *options.App, _ *App) {
	o.Windows = &woptions.Options{
		Theme:                             woptions.SystemDefault,
		DisableFramelessWindowDecorations: false, // 保留系统阴影与圆角
	}
}

// userConfigDir 当前用户的配置目录(界面偏好放这儿)。
func userConfigDir() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		base = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming")
	}
	return base
}

func desktopDir() string { return filepath.Join(os.Getenv("USERPROFILE"), "Desktop") }

func revealFile(path string) { _ = exec.Command("explorer.exe", "/select,", path).Start() }

// OpenLogs 打开日志目录。
func (a *App) OpenLogs() error { return exec.Command("explorer.exe", paths.Logs()).Start() }

// serviceBinary 后台服务的可执行文件,跟客户端装在同一个目录。
func serviceBinary(dir string) (string, error) {
	p := filepath.Join(dir, "godusevpn-svc.exe")
	if _, err := os.Stat(p); err != nil {
		return "", errors.New("找不到 godusevpn-svc.exe,请重新安装")
	}
	return p, nil
}

// applyUpdatePackage 以普通身份启动安装包,让它自己弹 UAC:Inno 会留一个未提权的进程当"原始用户",
// 装完才能以该用户重新拉起客户端(若在这里用 runas 直接提权,Inno 就不知道原始用户是谁,装完拉不起来)。
// 安装包会先杀掉本进程再覆盖文件。
func applyUpdatePackage(path string) error {
	return shellExec("open", path, "/VERYSILENT /SUPPRESSMSGBOXES /NORESTART /RELAUNCH=1", windows.SW_SHOWNORMAL)
}

// runElevated 以管理员身份跑一条命令(弹 UAC)。
func runElevated(exe, args string) error {
	return shellExec("runas", exe, args, windows.SW_HIDE)
}

func shellExec(verb, exe, args string, show int32) error {
	v, _ := syscall.UTF16PtrFromString(verb)
	file, _ := syscall.UTF16PtrFromString(exe)
	arg, _ := syscall.UTF16PtrFromString(args)
	dir, _ := syscall.UTF16PtrFromString(filepath.Dir(exe))
	return windows.ShellExecute(0, v, file, arg, dir, show)
}
