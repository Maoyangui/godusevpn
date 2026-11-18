// Package desktop Linux 桌面环境的集成:应用菜单里的「佛跳墙」图标(点开就是面板)、godusevpn:// 链接的登记
// (落地页的一键导入),以及给它们用的「面板现在开在哪」的提示文件。
// 软路由、没有桌面的服务器上没有 /usr/share/applications,这里什么都不做。
package desktop

import (
	"errors"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Maoyangui/godusevpn/internal/deeplink"
)

// 这几个位置可以在测试里换掉。
var (
	appsDir  = "/usr/share/applications"
	iconsDir = "/usr/share/icons/hicolor/scalable/apps"
	hintPath = "/run/godusevpn.panel"
)

const (
	entryName = "godusevpn.desktop"
	iconName  = "godusevpn.svg"
)

// Entry 桌面入口文件的内容。一个入口两个用途:应用菜单里点它 = 打开面板(open-link 不带参数);
// 它登记了 x-scheme-handler/godusevpn,浏览器里点 godusevpn:// 链接时系统把链接作为 %u 交给它。
func Entry(exe string) string {
	return strings.Join([]string{
		"[Desktop Entry]",
		"Type=Application",
		"Name=Godusevpn",
		"Name[zh_CN]=佛跳墙",
		"Name[zh_TW]=佛跳牆",
		"Comment=Open the Godusevpn panel",
		"Comment[zh_CN]=打开佛跳墙面板",
		"Exec=" + quoteExec(exe) + " open-link %u",
		"Icon=godusevpn",
		"Terminal=false",
		"Categories=Network;",
		"MimeType=x-scheme-handler/" + deeplink.Scheme + ";",
		"StartupNotify=false",
		"",
	}, "\n")
}

// quoteExec 按 desktop entry 规范给程序路径加引号(路径里有空格等保留字符时才需要)。
func quoteExec(p string) string {
	if !strings.ContainsAny(p, " \t\n\"'\\><~|&;$*?#()`") {
		return p
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", `$`, `\$`)
	return `"` + r.Replace(p) + `"`
}

// Available 这台机器有没有桌面的应用目录(没有就不登记)。
func Available() bool {
	st, err := os.Stat(appsDir)
	return err == nil && st.IsDir()
}

// Install 写入桌面入口和图标,刷新系统的登记。内容没变就不动(守护进程每次启动都会来对一遍)。
// 返回这次有没有改动。没有桌面的机器返回 false、nil。
func Install(exe string, icon []byte) (bool, error) {
	if !Available() {
		return false, nil
	}
	changed := false
	entry := filepath.Join(appsDir, entryName)
	if cur, err := os.ReadFile(entry); err != nil || string(cur) != Entry(exe) {
		if err := os.WriteFile(entry, []byte(Entry(exe)), 0o644); err != nil {
			return false, err
		}
		changed = true
	}
	if len(icon) > 0 {
		p := filepath.Join(iconsDir, iconName)
		if cur, err := os.ReadFile(p); err != nil || string(cur) != string(icon) {
			if err := os.MkdirAll(iconsDir, 0o755); err == nil {
				if os.WriteFile(p, icon, 0o644) == nil {
					changed = true
				}
			}
		}
	}
	if changed {
		refresh()
	}
	return changed, nil
}

// Uninstall 删掉桌面入口和图标。
func Uninstall() {
	removed := false
	for _, p := range []string{filepath.Join(appsDir, entryName), filepath.Join(iconsDir, iconName)} {
		if os.Remove(p) == nil {
			removed = true
		}
	}
	_ = os.Remove(hintPath)
	if removed {
		refresh()
	}
}

// refresh 让系统重新读应用目录(协议登记在 mimeinfo.cache 里)和图标缓存;命令没有就算了,多数桌面自己会发现。
func refresh() {
	if p, err := exec.LookPath("update-desktop-database"); err == nil {
		_ = exec.Command(p, "-q", appsDir).Run()
	}
	if p, err := exec.LookPath("gtk-update-icon-cache"); err == nil {
		_ = exec.Command(p, "-q", "-t", "-f", filepath.Dir(filepath.Dir(iconsDir))).Run()
	}
}

// WriteHint 守护进程把面板开在哪记下来(所有人可读:只是一个本机地址,不是秘密)。
// 点链接的是桌面上的普通用户,读不了 root 的设置文件、也连不了控制口,靠这个文件知道面板在哪。listen 为空 = 面板没开,删掉。
func WriteHint(listen string) {
	u := deeplink.LocalURL(listen)
	if u == "" {
		_ = os.Remove(hintPath)
		return
	}
	tmp := hintPath + ".tmp"
	if os.WriteFile(tmp, []byte(u+"\n"), 0o644) == nil {
		_ = os.Chmod(tmp, 0o644) // 不受 umask 影响
		_ = os.Rename(tmp, hintPath)
	}
}

// PanelURL 面板在本机的地址;守护进程没在跑(没有提示文件)返回空。
func PanelURL() string {
	b, err := os.ReadFile(hintPath)
	if err != nil {
		return ""
	}
	u := strings.TrimSpace(string(b))
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return ""
	}
	return u
}

// Alive 面板这个地址现在连不连得上(提示文件可能是进程被强杀后留下的)。
func Alive(panelURL string) bool {
	u, err := url.Parse(panelURL)
	if err != nil || u.Host == "" {
		return false
	}
	c, err := net.DialTimeout("tcp", u.Host, 800*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// openers 依次试的「用默认浏览器打开」的命令。
var openers = [][]string{{"xdg-open"}, {"gio", "open"}, {"sensible-browser"}, {"x-www-browser"}}

// run 可以在测试里换掉。
var run = func(name string, args ...string) error { return exec.Command(name, args...).Start() }

// Open 用默认浏览器打开一个地址。
func Open(u string) error {
	if b := strings.TrimSpace(os.Getenv("BROWSER")); b != "" && !strings.ContainsAny(b, " :%") {
		if p, err := exec.LookPath(b); err == nil {
			return run(p, u)
		}
	}
	for _, o := range openers {
		p, err := exec.LookPath(o[0])
		if err != nil {
			continue
		}
		return run(p, append(append([]string{}, o[1:]...), u)...)
	}
	return errors.New("没找到能打开浏览器的命令(xdg-open / gio);请手动在浏览器里打开 " + u)
}

// Notify 给桌面用户弹一条通知(点链接时没有终端可以打字);没有 notify-send 就算了。
func Notify(title, body string) {
	if p, err := exec.LookPath("notify-send"); err == nil {
		_ = run(p, "-a", "Godusevpn", "-i", "godusevpn", title, body)
	}
}
