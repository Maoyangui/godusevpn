// macOS:守护进程由 launchd 以 root 拉起(建 utun、改路由都要 root)。
// 服务描述文件在 /Library/LaunchDaemons/<label>.plist,开机自启就是 plist 里的 RunAtLoad,
// 所以"登录时自动启动"这一项在 macOS 上等价于服务本身是否启用,与 Linux 的语义一致。

package svc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const DisplayName = "佛跳墙"
const name = "godusevpn"

// label launchd 里的服务名;plist 文件名与它一致。
const label = "com.maoyangui." + name

func plistPath() string { return "/Library/LaunchDaemons/" + label + ".plist" }

// Kind 面板"服务状态"里显示的初始化系统。
func Kind() string { return "launchd" }

// IsService 是不是被 launchd 拉起来的(launchd 会给子进程这个环境变量)。
func IsService() bool {
	return os.Getenv("XPC_SERVICE_NAME") != "" && os.Getenv("XPC_SERVICE_NAME") != "0"
}

// Run 前台跑,收到 TERM / INT 时把 ctx 取消掉;重启交给 launchd 的 KeepAlive。
func Run(run func(ctx context.Context) error) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx)
}

func sh(bin string, args ...string) (string, error) {
	out, err := exec.Command(bin, args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		if s != "" {
			return s, fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err, s)
		}
		return s, fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), err)
	}
	return s, nil
}

func needRoot() error {
	if os.Geteuid() != 0 {
		return errors.New("需要 root 权限,请加 sudo")
	}
	return nil
}

// plist RunAtLoad 开机自启;KeepAlive 只在异常退出时重启(SuccessfulExit=false),
// 这样用户主动 stop 之后不会被 launchd 立刻拉起来。日志交给程序自己写,这里只兜住启动阶段的输出。
const plistTmpl = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>run</string>
	</array>
	<key>RunAtLoad</key><%s/>
	<key>KeepAlive</key>
	<dict><key>SuccessfulExit</key><false/></dict>
	<key>ProcessType</key><string>Interactive</string>
	<key>SoftResourceLimits</key>
	<dict><key>NumberOfFiles</key><integer>1048576</integer></dict>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`

func writePlist(exe string, runAtLoad bool) error {
	boot := "false"
	if runAtLoad {
		boot = "true"
	}
	errLog := filepath.Join("/Library/Logs", name+".launchd.log")
	body := fmt.Sprintf(plistTmpl, label, exe, boot, errLog)
	return os.WriteFile(plistPath(), []byte(body), 0o644)
}

// Install 写 plist 并交给 launchd;已经装过就先卸掉再装,保证指向当前这个程序。
func Install(exe string) error {
	if err := needRoot(); err != nil {
		return err
	}
	abs, err := filepath.Abs(exe)
	if err != nil {
		return err
	}
	_ = bootout() // 换了路径或重装:先把旧的踢掉,忽略"本来就没装"
	if err := writePlist(abs, true); err != nil {
		return err
	}
	return bootstrap()
}

// bootstrap / bootout 用新版 launchctl 的写法;老系统(10.10 以下)才需要 load/unload,这里不再兼容。
func bootstrap() error {
	_, err := sh("launchctl", "bootstrap", "system", plistPath())
	return err
}

func bootout() error {
	_, err := sh("launchctl", "bootout", "system/"+label)
	return err
}

// Uninstall 停止并删除服务。
func Uninstall() error {
	if err := needRoot(); err != nil {
		return err
	}
	_ = bootout()
	if err := os.Remove(plistPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Start 起服务;没装过 plist 的先报清楚。
func Start() error {
	if err := needRoot(); err != nil {
		return err
	}
	if _, err := os.Stat(plistPath()); err != nil {
		return errors.New("服务没安装,先执行 sudo " + name + " install")
	}
	if !loaded() {
		if err := bootstrap(); err != nil {
			return err
		}
	}
	_, err := sh("launchctl", "kickstart", "-k", "system/"+label)
	return err
}

// Stop 停服务:把它从 launchd 里踢出去,免得 KeepAlive 又拉起来;plist 留着,下次 start 再装回去。
func Stop() error {
	if err := needRoot(); err != nil {
		return err
	}
	if !loaded() {
		return nil
	}
	if err := bootout(); err != nil {
		return err
	}
	for i := 0; i < 50 && loaded(); i++ { // bootout 是异步的,等它真的退出
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

func StartUser() error { return Start() }
func StopUser() error  { return Stop() }

func loaded() bool {
	_, err := sh("launchctl", "print", "system/"+label)
	return err == nil
}

// QueryStatus running / stopped / not-installed,与 Linux 那份取值一致。
func QueryStatus() string {
	if _, err := os.Stat(plistPath()); err != nil {
		return "not-installed"
	}
	out, err := sh("launchctl", "print", "system/"+label)
	if err != nil {
		return "stopped"
	}
	// launchctl print 里有 "state = running" 才算真的在跑;只是装上没跑时是 "state = not running"
	if strings.Contains(out, "state = running") || strings.Contains(out, "pid = ") {
		return "running"
	}
	return "stopped"
}

func Status() string { return QueryStatus() }

// Enabled 开机自启:plist 里的 RunAtLoad 为真,且没有被 launchctl disable 掉。
func Enabled() bool {
	b, err := os.ReadFile(plistPath())
	if err != nil {
		return false
	}
	if !strings.Contains(string(b), "<key>RunAtLoad</key><true/>") {
		return false
	}
	if out, err := sh("launchctl", "print-disabled", "system"); err == nil {
		if strings.Contains(out, `"`+label+`" => disabled`) || strings.Contains(out, `"`+label+`" => true`) {
			return false
		}
	}
	return true
}

// SetEnabled 改开机自启:改写 plist 的 RunAtLoad,并同步 launchd 的 enable / disable 状态。
func SetEnabled(on bool) error {
	if err := needRoot(); err != nil {
		return err
	}
	b, err := os.ReadFile(plistPath())
	if err != nil {
		return errors.New("服务没安装,先执行 sudo " + name + " install")
	}
	exe := exeFromPlist(string(b))
	if exe == "" {
		return errors.New("读不出 plist 里的程序路径,请重新安装服务")
	}
	running := QueryStatus() == "running"
	if err := writePlist(exe, on); err != nil {
		return err
	}
	verb := "enable"
	if !on {
		verb = "disable"
	}
	_, _ = sh("launchctl", verb, "system/"+label)
	// plist 改了要重新载入才生效;本来在跑的就把它接着跑起来
	_ = bootout()
	if err := bootstrap(); err != nil {
		return err
	}
	if running {
		_, _ = sh("launchctl", "kickstart", "system/"+label)
	}
	return nil
}

// exeFromPlist 从 plist 里取回程序路径(ProgramArguments 的第一项)。
func exeFromPlist(s string) string {
	i := strings.Index(s, "<key>ProgramArguments</key>")
	if i < 0 {
		return ""
	}
	rest := s[i:]
	start := strings.Index(rest, "<string>")
	if start < 0 {
		return ""
	}
	rest = rest[start+len("<string>"):]
	end := strings.Index(rest, "</string>")
	if end < 0 {
		return ""
	}
	return rest[:end]
}
