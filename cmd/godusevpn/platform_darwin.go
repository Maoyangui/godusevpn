// macOS 上跟系统打交道的那几件事:提权、装更新、在访达里定位文件。
//
// 与 Windows 的两点不同:
//   - 提权没有 UAC,用 osascript 的 "with administrator privileges",系统会弹原生的管理员密码框;
//   - 更新包是 tar.gz(里面是守护进程二进制和 .app),解开之后覆盖 /usr/local/bin 与 /Applications,
//     再让 launchd 重新加载服务;这一整串写成一条命令交给上面那次提权,只弹一次密码框。
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

// 装好之后这两个位置是固定的:install.sh 与安装包都往这儿放。
const (
	daemonPath = "/usr/local/bin/godusevpn"
	appPath    = "/Applications/" + appBundleName
	// appBundleName .app 的目录名。用英文名,免得访达之外的地方(命令行、脚本)处理中文路径出岔子。
	appBundleName = "godusevpn.app"
)

// tuneOptions 补上 macOS 独有的窗口选项:用系统标题栏与红绿灯按钮,
// 页面的自绘标题栏是给 Windows 的,在 macOS 上让位给系统那套。
func tuneOptions(o *options.App, app *App) {
	o.Mac = &mac.Options{
		TitleBar:             mac.TitleBarHiddenInset(), // 不要系统标题栏,只留左上角的红绿灯;页面顶栏给它们让出位置
		WebviewIsTransparent: false,
		WindowIsTranslucent:  false,
		About: &mac.AboutInfo{
			Title:   buildinfo.DisplayName,
			Message: "版本 " + buildinfo.Version,
		},
		// 落地页的一键导入:macOS 由系统按 Info.plist 里登记的 godusevpn:// 把地址交给这里
		OnUrlOpen: func(u string) { app.secondInstance(u) },
	}
	// macOS 下用系统标题栏和红绿灯:自绘那套是给 Windows 的,在这儿反而没法拖窗口。
	o.Frameless = false
	// 没有菜单栏图标,就不能把窗口藏起来 —— 藏了用户找不回来。关窗口只是关界面,隧道在后台服务里照常跑。
	o.HideWindowOnClose = false
}

// userConfigDir 当前用户的配置目录。
func userConfigDir() string {
	return filepath.Join(os.Getenv("HOME"), "Library", "Application Support")
}

func desktopDir() string { return filepath.Join(os.Getenv("HOME"), "Desktop") }

func revealFile(path string) { _ = exec.Command("open", "-R", path).Start() }

// OpenLogs 打开日志目录。
func (a *App) OpenLogs() error { return exec.Command("open", paths.Logs()).Start() }

// serviceBinary 守护进程的可执行文件。图形界面在 /Applications 里,守护进程在 /usr/local/bin,
// 两个位置分开;开发时也允许它就放在 .app 旁边。
func serviceBinary(dir string) (string, error) {
	for _, p := range []string{daemonPath, filepath.Join(dir, "godusevpn")} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("找不到 " + daemonPath + ",请重新安装")
}

// applyUpdatePackage 解开下载好的 tar.gz,再用一次提权把守护进程与图形界面一起换掉并重启服务。
func applyUpdatePackage(path string) error {
	dir := filepath.Join(filepath.Dir(path), "unpacked")
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if out, err := exec.Command("tar", "-xzf", path, "-C", dir).CombinedOutput(); err != nil {
		return fmt.Errorf("解压更新包失败: %s", strings.TrimSpace(string(out)))
	}
	newDaemon := filepath.Join(dir, "godusevpn")
	if _, err := os.Stat(newDaemon); err != nil {
		return errors.New("更新包里没有守护进程,可能下错了架构")
	}
	// 顺序:先停服务再换文件,换完重新装一遍(launchd 的 plist 里记着路径,重装才会重新加载)
	script := fmt.Sprintf("%s uninstall; /usr/bin/install -m755 %s %s", sh(daemonPath), sh(newDaemon), sh(daemonPath))
	if newApp := filepath.Join(dir, appBundleName); dirExists(newApp) {
		script += fmt.Sprintf("; /bin/rm -rf %s; /bin/cp -R %s %s", sh(appPath), sh(newApp), sh(appPath))
	}
	script += fmt.Sprintf("; %s install", sh(daemonPath))
	if err := runElevatedScript(script); err != nil {
		return err
	}
	// 新版界面已经就位,退出让用户重新打开(自己重启会和刚被覆盖的 .app 打架)
	go func() { os.Exit(0) }()
	return nil
}

// runElevated 以管理员身份跑一条命令(弹系统的密码框)。
func runElevated(exe, args string) error {
	return runElevatedScript(sh(exe) + " " + args)
}

func runElevatedScript(script string) error {
	// osascript 里那层字符串要按 AppleScript 的规矩转义
	as := strings.ReplaceAll(script, `\`, `\`)
	as = strings.ReplaceAll(as, `"`, `\"`)
	out, err := exec.Command("osascript", "-e", `do shell script "`+as+`" with administrator privileges`).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "-128") { // 用户点了取消
			return errors.New("已取消")
		}
		return fmt.Errorf("需要管理员权限: %s", msg)
	}
	return nil
}

// sh 给 shell 用的单引号转义。
func sh(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func dirExists(p string) bool { st, err := os.Stat(p); return err == nil && st.IsDir() }
