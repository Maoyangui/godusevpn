//go:build !windows

// Linux 的"服务":按初始化系统落地为 systemd 单元、OpenWrt 的 procd 脚本或 Entware(梅林)的 init.d 脚本。
// 对外接口与 Windows 版一致(Install / Uninstall / Start / Stop / Status / QueryStatus …)。
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

	"github.com/Maoyangui/godusevpn/internal/paths"
)

const DisplayName = "佛跳墙"
const name = "godusevpn"

type initKind int

const (
	initNone initKind = iota
	initSystemd
	initProcd
	initEntware
)

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// detect 判断当前初始化系统:OpenWrt(procd)、梅林等 Entware、systemd。
func detect() initKind {
	switch {
	case exists("/etc/openwrt_release") || (exists("/sbin/procd") && exists("/etc/rc.common")):
		return initProcd
	case exists("/opt/etc/init.d") && !exists("/run/systemd/system"):
		return initEntware
	case exists("/run/systemd/system"):
		return initSystemd
	}
	return initNone
}

// Kind 初始化系统名字(诊断与安装提示用)。
func Kind() string {
	switch detect() {
	case initSystemd:
		return "systemd"
	case initProcd:
		return "procd"
	case initEntware:
		return "entware"
	}
	return "none"
}

// IsService 是否由初始化系统拉起(systemd 会带 INVOCATION_ID)。
func IsService() bool { return os.Getenv("INVOCATION_ID") != "" }

// Run 前台跑到收到 SIGINT / SIGTERM。
func Run(run func(ctx context.Context) error) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx)
}

func sh(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		if s != "" {
			return s, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), s)
		}
		return s, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return s, nil
}

func needRoot() error {
	if os.Geteuid() != 0 {
		return errors.New("需要 root 权限(sudo)")
	}
	return nil
}

const systemdUnit = `[Unit]
Description=佛跳墙 (godusevpn)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s run
Restart=on-failure
RestartSec=3
LimitNOFILE=1048576
KillMode=mixed
TimeoutStopSec=15

[Install]
WantedBy=multi-user.target
`

// 开机闸:nft 的表、sysctl 改的网卡 IPv6 都不过重启,开机到服务起来之间要靠它 —— 早于联网把落盘的闸装上、
// 按备份先停网卡 IPv6(见 netmode.ApplyBootGuard)。两个文件都不在(没在严格全局、没停过网卡 IPv6)就整个跳过:
// ConditionPathExists 前面的 | 是"任一满足"。
const guardName = name + "-guard"

const systemdGuardUnit = `[Unit]
Description=佛跳墙开机闸 (godusevpn)
DefaultDependencies=no
After=local-fs.target
Before=network-pre.target shutdown.target
Wants=network-pre.target
Conflicts=shutdown.target
RequiresMountsFor=%[2]s %[3]s
ConditionPathExists=|%[2]s/guard-boot.nft
ConditionPathExists=|%[2]s/nic-ipv6-backup.json

[Service]
Type=oneshot
ExecStart=%[1]s boot-guard
RemainAfterExit=yes

[Install]
WantedBy=sysinit.target
`

// procdGuardScript OpenWrt:network 是 START=20,同为 19 的 firewall(fw4 只重建自己那张表)按名字排在前面。只在开机时做。
const procdGuardScript = `#!/bin/sh /etc/rc.common
# 佛跳墙开机闸 (godusevpn)
START=19

boot() {
	%s boot-guard
}

start() {
	return 0
}
`

func guardUnitPath(k initKind) string {
	switch k {
	case initSystemd:
		return "/etc/systemd/system/" + guardName + ".service"
	case initProcd:
		return "/etc/init.d/" + guardName
	}
	return ""
}

// guardUnitText 当前版本的开机闸单元;Entware 做不到开机闸(/opt 挂得比联网晚),给空。
func guardUnitText(k initKind, exe string) string {
	switch k {
	case initSystemd:
		return fmt.Sprintf(systemdGuardUnit, exe, paths.DataDir(), filepath.Dir(exe))
	case initProcd:
		return fmt.Sprintf(procdGuardScript, exe)
	}
	return ""
}

// ensureGuardUnit 开机闸单元在且是当前内容、启用着;内容不同才写。
func ensureGuardUnit(k initKind, exe string) error {
	text, p := guardUnitText(k, exe), guardUnitPath(k)
	if text == "" {
		return nil
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != text {
		mode := os.FileMode(0o644)
		if k == initProcd {
			mode = 0o755
		}
		if err := os.WriteFile(p, []byte(text), mode); err != nil {
			return err
		}
		if k == initSystemd {
			if _, err := sh("systemctl", "daemon-reload"); err != nil {
				return err
			}
		}
	}
	if k == initSystemd {
		_, err := sh("systemctl", "enable", guardName)
		return err
	}
	_, err := sh(p, "enable")
	return err
}

func removeGuardUnit(k initKind) {
	p := guardUnitPath(k)
	if p == "" || !exists(p) {
		return
	}
	if k == initSystemd {
		_, _ = sh("systemctl", "disable", guardName)
	} else {
		_, _ = sh(p, "disable")
	}
	_ = os.Remove(p)
}

const procdScript = `#!/bin/sh /etc/rc.common
# 佛跳墙 (godusevpn)
START=99
STOP=10
USE_PROCD=1

start_service() {
	procd_open_instance
	procd_set_param command %s run
	procd_set_param respawn 3600 5 0
	procd_set_param stdout 1
	procd_set_param stderr 1
	procd_close_instance
}
`

const entwareScript = `#!/bin/sh
# 佛跳墙 (godusevpn),Entware 的 rc.func 约定:PROCS 是 /opt/bin 里的可执行文件名
ENABLED=yes
PROCS=godusevpn
ARGS="run"
PREARGS=""
DESC=$PROCS
PATH=/opt/bin:/opt/sbin:/sbin:/bin:/usr/sbin:/usr/bin

# 梅林没有 systemd / procd 那样的崩溃重拉:start 时挂一条每分钟的 cru 定时,服务没在跑就拉起来。
# 用户主动 stop 留个标记,定时看到它就不拉;再 start(开机也是 start)时去掉。
STOPPED=/opt/var/lib/godusevpn/stopped
case "$1" in
watchdog)
	[ "$ENABLED" = yes ] || exit 0
	[ -f "$STOPPED" ] && exit 0
	pidof $PROCS >/dev/null && exit 0
	set -- start
	;;
start | restart)
	rm -f "$STOPPED"
	command -v cru >/dev/null 2>&1 && cru a $PROCS "* * * * * /opt/etc/init.d/S99godusevpn watchdog"
	;;
stop | kill)
	mkdir -p "${STOPPED%/*}" && touch "$STOPPED"
	;;
esac

. /opt/etc/init.d/rc.func
`

// entwareWatchdog 看门狗定时的 cru 编号与命令(和脚本里那行一致)。
var entwareWatchdog = []string{"a", name, "* * * * * /opt/etc/init.d/S99" + name + " watchdog"}

func unitPath() string {
	switch detect() {
	case initSystemd:
		return "/etc/systemd/system/" + name + ".service"
	case initProcd:
		return "/etc/init.d/" + name
	case initEntware:
		return "/opt/etc/init.d/S99" + name
	}
	return ""
}

// Install 注册开机自启(不启动)。exe 是本程序的绝对路径。
func Install(exe string) error {
	if err := needRoot(); err != nil {
		return err
	}
	switch k := detect(); k {
	case initSystemd:
		if err := os.WriteFile(unitPath(), []byte(fmt.Sprintf(systemdUnit, exe)), 0o644); err != nil {
			return err
		}
		if _, err := sh("systemctl", "daemon-reload"); err != nil {
			return err
		}
		if _, err := sh("systemctl", "enable", name); err != nil {
			return err
		}
		return ensureGuardUnit(k, exe)
	case initProcd:
		if err := os.WriteFile(unitPath(), []byte(fmt.Sprintf(procdScript, exe)), 0o755); err != nil {
			return err
		}
		if _, err := sh(unitPath(), "enable"); err != nil {
			return err
		}
		return ensureGuardUnit(k, exe)
	case initEntware:
		// rc.func 按名字找进程,程序必须叫 godusevpn 且在 /opt/bin 里
		target := "/opt/bin/" + name
		if abs, _ := filepath.Abs(exe); abs != target {
			_ = os.Remove(target)
			if err := os.Symlink(abs, target); err != nil {
				return fmt.Errorf("建 %s 链接: %w", target, err)
			}
		}
		return os.WriteFile(unitPath(), []byte(entwareScript), 0o755)
	}
	return errors.New("没识别出初始化系统(systemd / procd / Entware),请手动配置自启:" + exe + " run")
}

// Uninstall 停止并删除自启。
func Uninstall() error {
	if err := needRoot(); err != nil {
		return err
	}
	_ = Stop()
	switch k := detect(); k {
	case initSystemd:
		_, _ = sh("systemctl", "disable", name)
		_ = os.Remove(unitPath())
		removeGuardUnit(k)
		_, _ = sh("systemctl", "daemon-reload")
	case initProcd:
		if exists(unitPath()) {
			_, _ = sh(unitPath(), "disable")
		}
		_ = os.Remove(unitPath())
		removeGuardUnit(k)
	case initEntware:
		if _, err := exec.LookPath("cru"); err == nil {
			_, _ = sh("cru", "d", name) // 看门狗定时
		}
		_ = os.Remove(unitPath())
		_ = os.Remove("/opt/bin/" + name)
	}
	return nil
}

// Refresh 守护进程每次启动时调:面板自更新只换程序文件、不重跑 install,旧版装下的启动脚本会一直留着。
// 只动已经装好的(手动 `godusevpn run`、没装服务的不碰)。systemd / procd 补上开机闸单元;
// Entware 的脚本(看门狗)不带程序路径,和当前模板不同就按原来的开关状态重写(先写临时文件再改名:脚本可能正被 sh 读着),
// 再补上看门狗定时 —— 服务正在起,说明用户没停它。
func Refresh() error {
	k := detect()
	if !exists(unitPath()) {
		return nil
	}
	if k == initSystemd || k == initProcd {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if p, err := filepath.EvalSymlinks(exe); err == nil {
			exe = p
		}
		return ensureGuardUnit(k, exe)
	}
	if k != initEntware {
		return nil
	}
	b, err := os.ReadFile(unitPath())
	if err != nil {
		return err
	}
	want, enabled := entwareScriptLike(string(b))
	if string(b) != want {
		tmp := unitPath() + ".tmp"
		if err := os.WriteFile(tmp, []byte(want), 0o755); err != nil {
			return err
		}
		if err := os.Rename(tmp, unitPath()); err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	if _, err := exec.LookPath("cru"); err != nil || !enabled {
		return nil // 不是梅林(没有 cru),或者自启关着
	}
	_, err = sh("cru", entwareWatchdog...)
	return err
}

// entwareScriptLike 当前模板,开机自启的开关照抄已装的那份。
func entwareScriptLike(cur string) (script string, enabled bool) {
	if strings.Contains(cur, "ENABLED=yes") {
		return entwareScript, true
	}
	return strings.Replace(entwareScript, "ENABLED=yes", "ENABLED=no", 1), false
}

func ctl(action string) error {
	if err := needRoot(); err != nil {
		return err
	}
	switch detect() {
	case initSystemd:
		_, err := sh("systemctl", action, name)
		return err
	case initProcd, initEntware:
		if !exists(unitPath()) {
			return errors.New("服务未安装")
		}
		_, err := sh(unitPath(), action)
		return err
	}
	return errors.New("没识别出初始化系统")
}

func Start() error { return ctl("start") }

// Restart 让初始化系统重启本服务,交出去就返回。调用方可能正是这个服务自己(应用内升级换完文件):
// 在本进程里先 Stop 再 Start,Stop 就把自己杀了,Start 永远轮不到,服务一直停着。
func Restart() error {
	if err := needRoot(); err != nil {
		return err
	}
	k := detect()
	if k != initSystemd && !exists(unitPath()) {
		return errors.New("服务未安装")
	}
	argv, detach := restartCmd(k, unitPath())
	if argv == nil {
		return errors.New("没识别出初始化系统")
	}
	if !detach {
		_, err := sh(argv[0], argv[1:]...)
		return err
	}
	// procd / Entware 的 restart 是脚本里的 stop 再 start:脱离本进程跑(自成一个会话),本进程被停掉它也跑得完。
	// Entware 的 stop 按进程名 killall godusevpn,这个脚本进程不叫这个名字,杀不到它。
	c := exec.Command(argv[0], argv[1:]...)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}

// restartCmd 各初始化系统的重启命令,以及要不要脱离本进程去跑。
// systemd 的 --no-block 把重启任务交给 systemd 自己,发起的进程被杀也照样执行完(KillMode=mixed 会连带杀掉
// 本服务 cgroup 里的子进程,所以不能靠脱离子进程)。
func restartCmd(k initKind, unit string) (argv []string, detach bool) {
	switch k {
	case initSystemd:
		return []string{"systemctl", "--no-block", "restart", name}, false
	case initProcd, initEntware:
		return []string{unit, "restart"}, true
	}
	return nil, false
}

// Stop is idempotent so a first install can safely run the same fail-closed
// upgrade path even when a binary was copied into place without a unit yet.
// Once a unit exists, every real control error is still returned to the
// caller; update scripts must never continue past an uncertain stop.
func Stop() error {
	if err := needRoot(); err != nil {
		return err
	}
	if unitPath() == "" || !exists(unitPath()) {
		return nil
	}
	return ctl("stop")
}

// StartUser / StopUser Windows 上给非管理员用;Linux 上同样需要 root。
func StartUser() error { return Start() }
func StopUser() error  { return Stop() }

// QueryStatus running / stopped / not-installed / starting。
func QueryStatus() string {
	if unitPath() == "" || !exists(unitPath()) {
		return "not-installed"
	}
	switch detect() {
	case initSystemd:
		out, _ := sh("systemctl", "is-active", name)
		switch out {
		case "active":
			return "running"
		case "activating":
			return "starting"
		}
		return "stopped"
	case initProcd:
		if _, err := sh(unitPath(), "running"); err == nil {
			return "running"
		}
		return "stopped"
	case initEntware:
		if _, err := sh("pidof", name); err == nil {
			return "running"
		}
		return "stopped"
	}
	return "stopped"
}

func Status() string { return QueryStatus() }

// Enabled 是否开机自启。
func Enabled() bool {
	switch detect() {
	case initSystemd:
		out, _ := sh("systemctl", "is-enabled", name)
		return out == "enabled"
	case initProcd:
		if _, err := sh(unitPath(), "enabled"); err == nil {
			return true
		}
		return false
	case initEntware:
		b, err := os.ReadFile(unitPath())
		return err == nil && strings.Contains(string(b), "ENABLED=yes")
	}
	return false
}

// SetEnabled 开关开机自启(服务本身不动)。
func SetEnabled(on bool) error {
	if err := needRoot(); err != nil {
		return err
	}
	switch detect() {
	case initSystemd:
		a := "disable"
		if on {
			a = "enable"
		}
		_, err := sh("systemctl", a, name)
		return err
	case initProcd:
		a := "disable"
		if on {
			a = "enable"
		}
		_, err := sh(unitPath(), a)
		return err
	case initEntware:
		b, err := os.ReadFile(unitPath())
		if err != nil {
			return err
		}
		s := strings.ReplaceAll(strings.ReplaceAll(string(b), "ENABLED=yes", "ENABLED=no"), "ENABLED=no", map[bool]string{true: "ENABLED=yes", false: "ENABLED=no"}[on])
		return os.WriteFile(unitPath(), []byte(s), 0o755)
	}
	return errors.New("没识别出初始化系统")
}
