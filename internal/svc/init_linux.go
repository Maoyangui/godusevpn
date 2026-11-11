//go:build !windows

// Linux 的"服务":按初始化系统落地为 systemd 单元、OpenWrt 的 procd 脚本或 Entware(梅林)的 init.d 脚本。
// 对外接口与 Windows 版一致(Install / Uninstall / Start / Stop / Status / QueryStatus …)。
package svc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
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
ExecStart=%[1]s run
ExecStopPost=-%[1]s post-stop
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
	procd_set_param respawn 3600 1 0
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

# 梅林没有 systemd / procd 那样的崩溃重拉:起服务时挂一条每分钟的 cru 定时,服务没在跑就拉起来。
# 用户主动 stop 留个标记,定时看到它就不拉;再起服务(开机也算)时去掉。
STOPPED=/opt/var/lib/godusevpn/stopped

# 开机自启的开关(ENABLED)只管开机,和 systemd / procd 一样:rc.unslung 开机时调的是 start,自启关着就照 rc.func
# 返回 8、不起,标记和定时也不动(手动跑 rc.unslung start、本脚本的 start 也一样)。godusevpn start / install 起服务调的是
# manual-start,不看开关;restart(面板升级后的重启、rc.unslung restart)自启关着时照样重启正在跑的,没在跑的不起、返回 8。
# 看门狗也不看开关:定时是起了服务才挂的(开机没起就没有),手动起来的崩了照样拉,和 systemd / procd 关了自启照样重拉一样。

# 起停、check、reconfigure 都不交给 rc.func:它按名字 pidof / killall $PROCS,而 godusevpn install / start / stop /
# uninstall 这些正在调本脚本的命令也叫这个名字 —— start 把调用者当成"已经在跑"而不拉(装完服务没起来,梅林要等看门狗
# 一分钟后拉,没有 cru 的要等重启);stop 把调用者一起杀掉(卸载做到一半自己没了,闸、自启脚本、看门狗定时全留着);
# check 只要有一条 godusevpn 命令在跑就说服务活着;reconfigure 给所有同名进程发 SIGHUP,守护进程不接这个信号、
# 收到就退出(调用者、别的 godusevpn 命令也一样)。
# 这里只认守护进程(拉起来的 "$PROCS $ARGS",命令行第二项是 $ARGS),按进程号发信号,调用者一个都不碰。
CALLER=$2
arg2() { { tr '\0' '\n' < /proc/$1/cmdline; } 2>/dev/null | sed -n 2p; }
daemons() {
	for p in $(pidof $PROCS); do
		[ "$(arg2 $p)" = "$ARGS" ] && echo $p
	done
}
# gone 停掉的这些进程都没了:/proc 里没有了,或者进程号已经给了别的程序。退出了、还没收尸的(僵尸)不算 ——
# 有的 pidof 把僵尸也当成在跑,停完紧接着只按名字查的(用户自己的脚本之类)会以为服务还在。
gone() {
	for p in "$@"; do
		[ -d /proc/$p ] || continue
		[ "$(sed -n 's/.*) \(.\) .*/\1/p' /proc/$p/stat 2>/dev/null)" = Z ] && return 1
		[ "$(arg2 $p)" = "$ARGS" ] && return 1
	done
	return 0
}
halt() {
	echo " Shutting down $DESC..."
	pids=$(daemons)
	[ -z "$pids" ] || kill -$1 $pids 2>/dev/null
	i=0
	until gone $pids; do
		if [ $i -ge 15 ]; then
			echo " $DESC is still running ($pids), failed."
			return 255
		fi
		sleep 1
		i=$((i + 1))
	done
	echo " done."
}
# 拉起那一段(查有没有在跑 → 没有就拉起 → 等它出现)同一时间只让一个脚本做:不然守护进程刚崩时,用户手动起服务恰好
# 撞上看门狗整分钟的那一下,两边都查到没在跑,就各拉起一个。stop / kill 也拿这把锁,写"用户停了"的标记和停守护进程
# 都在锁里(见下面 stop 那段)。BusyBox 和 Entware 上不保证有 flock,用 mkdir 建锁目录
# (建成了就是拿到了,是原子的),里面 owner 写持有者的进程号和开机秒数(/proc/uptime,不受改系统时间影响)。拿不到就
# 隔一秒再试;持有者已经不在了、锁拿了超过 LOCKTTL 秒(正常最多十几秒)、或者是重启前留下的(锁在 /opt 上,重启不丢),
# 当成没放掉的旧锁清掉。拿着锁时被 TERM / INT / HUP 打断也先放锁。
LOCK=/opt/var/run/godusevpn.lock
LOCKTTL=30
MYLOCK=
uptime_s() { read -r s _ < /proc/uptime; echo "${s%%.*}"; }
# stale 锁的记录($1:"进程号 开机秒数")作不作数:持有者不在了(退出了没收尸的也算;进程号是自己的,说明是重启前
# 留下的)、开机秒数比现在还大(重启前留下的)、或者拿了超过 LOCKTTL 秒,都不作数。记录不全(刚建还没写上、写坏了)
# 的不在这里判,由 lock 自己计时。
stale() {
	case "$1" in *" "*) ;; *) return 1 ;; esac
	pid=${1%% *} up=${1#* }
	case "$pid:$up" in *[!0-9:]* | :* | *: | 0?* | *:0?*) return 1 ;; esac
	now=$(uptime_s)
	[ "$now" -lt "$up" ] || [ $((now - up)) -gt $LOCKTTL ] || [ "$pid" = $$ ] || [ ! -d /proc/$pid ] && return 0
	[ "$(sed -n 's/.*) \(.\) .*/\1/p' /proc/$pid/stat 2>/dev/null)" = Z ]
}
lock() {
	trap unlock EXIT
	trap 'unlock; exit 129' HUP
	trap 'unlock; exit 130' INT
	trap 'unlock; exit 143' TERM
	mkdir -p "${LOCK%/*}" 2>/dev/null
	seen=- waited=0 tries=0
	# 记录在每次建目录之前算好,建成了紧接着写进去:建好目录到写上记录之间越短,别人看到"没记录"的锁越少
	until MYLOCK="$$ $(uptime_s)"; mkdir "$LOCK" 2>/dev/null; do
		if [ ! -d "$LOCK" ]; then
			# 锁目录不在却建不成(/opt 只读之类):不加锁,照以前那样起。刚被放掉的话下一圈就建成了
			tries=$((tries + 1))
			[ $tries -ge 3 ] && { MYLOCK=; return 0; }
			continue
		fi
		owner=$(cat "$LOCK/owner" 2>/dev/null)
		[ "$owner" = "$seen" ] || { seen=$owner; waited=0; }
		if stale "$owner" || [ $waited -ge $LOCKTTL ]; then
			# 清之前再看一眼还是不是这一把,别把别人刚建的新锁清掉
			[ "$(cat "$LOCK/owner" 2>/dev/null)" = "$owner" ] && rm -rf "$LOCK"
			continue
		fi
		sleep 1
		waited=$((waited + 1))
	done
	echo "$MYLOCK" > "$LOCK/owner" || { rm -rf "$LOCK"; MYLOCK=; }
}
unlock() {
	[ -n "$MYLOCK" ] && [ "$(cat "$LOCK/owner" 2>/dev/null)" = "$MYLOCK" ] && rm -rf "$LOCK"
	MYLOCK=
	trap - EXIT HUP INT TERM
	return 0
}
# 守护进程的标准错误接到 ERRLOG,不丢进 /dev/null:运行时崩溃(fatal error、panic)时把原因和全部 goroutine 的栈直接写到
# 标准错误,守护进程自己的 crash.log 只抄得到栈,fatal error 那一行原因只在这里。守护进程平时也往标准错误写 —— 内核在 info 级
# 给每条连接记一行目标 —— 它一起来看到标准错误接着这个文件,就把这些改去 /dev/null(连接目标默认不落盘),这里只剩崩溃信息
# 和每次拉起记的一行抬头。只给 root 看(0600);超过 ERRMAX 就在拉起时整份挪成 .1、只留一份旧的(一次崩溃的栈就可能几百 KB,
# 原因在最前面,截尾巴会把它截掉)。建不成(/opt 只读之类)就照旧接 /dev/null,照样起。
ERRLOG=/opt/var/log/godusevpn.stderr.log
ERRMAX=262144
errlog() {
	mkdir -p "${ERRLOG%/*}" 2>/dev/null && (umask 077 && : >> "$ERRLOG") 2>/dev/null || return 1
	size=$(wc -c < "$ERRLOG" 2>/dev/null | tr -dc 0-9)
	if [ "${size:-0}" -gt $ERRMAX ] && mv -f "$ERRLOG" "$ERRLOG.1" 2>/dev/null; then
		chmod 600 "$ERRLOG.1" 2>/dev/null
		(umask 077 && : >> "$ERRLOG") 2>/dev/null || return 1
	fi
	chmod 600 "$ERRLOG" 2>/dev/null
	echo "== $(date '+%Y-%m-%d %H:%M:%S') 由 ${CALLER:-$1} 拉起" >> "$ERRLOG" 2>/dev/null
}
# launch 照 rc.func 的 start:守护进程已经在跑就不再起(重复 start 没事);否则在后台拉起,等它出现,最多 10 秒,
# 出现了才算起来了。整段拿着锁(见上)。开机自启的开关不在这里看(只有开机的 start 看,见上)。
# "用户停了"的标记也在锁里看、在锁里去:看门狗($1 是 watchdog)拿到锁再看一次,有标记就不拉 —— 它在锁外看那一眼之后、
# 拿到锁之前,用户的 stop 可能已经写下标记停完了;起服务的去掉标记(用户要它起来),和 stop 在锁里写标记前后分明,
# 同时跑的 start 和 stop 谁后拿到锁就照谁的,不会出现停着、标记却被去掉(一分钟后看门狗又拉起来)的情形。
launch() {
	lock
	if [ "$1" = watchdog ]; then
		if [ -f "$STOPPED" ]; then
			unlock
			return 0
		fi
	else
		rm -f "$STOPPED"
	fi
	echo " Starting $DESC..."
	if [ -n "$(daemons)" ]; then
		unlock
		echo " $DESC is already running."
		return 0
	fi
	err=/dev/null
	errlog "$1" && err=$ERRLOG
	$PREARGS $PROCS $ARGS >/dev/null 2>>"$err" &
	i=0
	until [ -n "$(daemons)" ]; do
		if [ $i -ge 10 ]; then
			unlock
			echo " $DESC failed to start."
			logger "Failed to start $DESC from ${CALLER:-$1}." 2>/dev/null
			return 255
		fi
		sleep 1
		i=$((i + 1))
	done
	unlock
	logger "Started $DESC from ${CALLER:-$1}." 2>/dev/null
	echo " done."
}

case "$1" in
watchdog)
	[ -f "$STOPPED" ] && exit 0
	[ -n "$(daemons)" ] && exit 0
	launch watchdog
	exit $?
	;;
start | manual-start | restart | reconfigure)
	case "$1" in
	start) [ "$ENABLED" = yes ] || exit 8 ;;
	restart) [ "$ENABLED" = yes ] || [ -n "$(daemons)" ] || exit 8 ;;
	reconfigure)
		# rc.unslung reconfigure:重启守护进程,设置照新的重新读(rc.func 是按名字发 SIGHUP,见上)。
		# 没在跑就不动 —— rc.func 的 reconfigure 也只给在跑的进程发信号,从不拉起
		if [ -z "$(daemons)" ]; then
			echo " $DESC is not running."
			exit 0
		fi
		;;
	esac
	if [ "$1" = restart ] || [ "$1" = reconfigure ]; then
		halt TERM || exit $?
	fi
	launch "$1"
	rc=$?
	# 标记在 launch 里、拿着锁去掉;定时挂在拉起之后(拉起那一段有锁,看门狗赶上整分钟那一下也只会起一个)。
	# 没拉起来也照样挂,看门狗每分钟再试
	command -v cru >/dev/null 2>&1 && cru a $PROCS "* * * * * /opt/etc/init.d/S99godusevpn watchdog"
	exit $rc
	;;
stop | kill)
	# 写标记、停守护进程都拿着拉起那一段的锁:守护进程崩着时 stop 撞上看门狗,看门狗可能已经看过标记(还没写)、正在拉起,
	# 刚拉起的还没出现,不在锁里的话 stop 一个都停不到就报 done,守护进程随后起来、一直跑着。拿到锁时那边要么已经拉起来了
	# (这里停掉它),要么还没拿到锁(拿到后看到标记,不拉)
	lock
	mkdir -p "${STOPPED%/*}" && touch "$STOPPED"
	if [ "$1" = kill ]; then halt KILL; else halt TERM; fi
	rc=$?
	unlock
	exit $rc
	;;
check)
	if [ -n "$(daemons)" ]; then
		echo " Checking $DESC... alive."
		exit 0
	fi
	echo " Checking $DESC... dead."
	exit 1
	;;
esac

. /opt/etc/init.d/rc.func
`

// entwareWatchdog 看门狗定时的 cru 编号与命令(和脚本里那行一致)。
var entwareWatchdog = []string{"a", name, "* * * * * /opt/etc/init.d/S99" + name + " watchdog"}

// entwareManualStart 程序(godusevpn start / install)起服务时调 Entware 脚本的动作。脚本的 start 留给开机时的 rc.unslung,
// 照 rc.func 看开机自启的开关,关着就不起、返回 8;程序起服务和 systemd / procd 上一样不看开关,所以用脚本自己的这个动作。
const entwareManualStart = "manual-start"

// startAction 程序起服务时交给初始化系统的动作。
func startAction(k initKind) string {
	if k == initEntware {
		return entwareManualStart
	}
	return "start"
}

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
		// 重装时服务可能正在跑,看门狗的 sh 可能正读着旧脚本:换文件不原地改(见 replaceFile)
		return replaceFile(unitPath(), []byte(entwareScript), 0o755)
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
// 是启动脚本拉起来的就再补上看门狗定时 —— 服务正在起,说明用户没停它(前台跑的不补,见 refreshEntware)。
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
		if err := refreshMainUnit(k, exe); err != nil {
			return err
		}
		return ensureGuardUnit(k, exe)
	}
	if k != initEntware {
		return nil
	}
	return refreshEntware(unitPath())
}

// refreshEntware Refresh 在 Entware 上的那一段:p 是已装的启动脚本。
// 补挂看门狗定时是给旧版本的脚本擦屁股的(v0.7.9 之前的脚本起服务时不挂定时),只给启动脚本拉起来的守护进程补:
// 用户在终端里前台跑的 godusevpn run(调试)补挂了的话,Ctrl-C 停掉之后一分钟内就被看门狗拉到后台去 —— 前台停掉
// 不留"用户停了"的标记,看门狗当它崩了。
func refreshEntware(p string) error {
	enabled, err := refreshEntwareScript(p)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("cru"); err != nil || !enabled || !launchedByScript() {
		// 不是梅林(没有 cru);自启关着(这时在跑的是用户手动起的,当前的脚本起它的时候已经挂上了);
		// 或者不是启动脚本拉起来的(前台跑的)
		return nil
	}
	_, err = sh("cru", entwareWatchdog...)
	return err
}

// launchedByScript 本进程是不是启动脚本在后台拉起来的:新旧脚本(连同 Entware 的 rc.func)都是
// "$PROCS $ARGS >/dev/null ... &",标准输出接 /dev/null,sh 给后台命令的标准输入也是 /dev/null;在终端里前台跑的
// 标准输入输出连着终端(输出接到管道、文件的也不算)。
func launchedByScript() bool { return isDevNull(os.Stdin) && isDevNull(os.Stdout) }

// isDevNull f 是不是 /dev/null。
func isDevNull(f *os.File) bool {
	a, err := f.Stat()
	if err != nil {
		return false
	}
	b, err := os.Stat(os.DevNull)
	if err != nil {
		return false
	}
	if os.SameFile(a, b) {
		return true
	}
	as, aok := a.Sys().(*syscall.Stat_t)
	bs, bok := b.Sys().(*syscall.Stat_t)
	return aok && bok && a.Mode()&os.ModeCharDevice != 0 && b.Mode()&os.ModeCharDevice != 0 && as.Rdev == bs.Rdev
}

// refreshEntwareScript 已装的 Entware 脚本(p)和当前模板不同就按原来的开关状态重写(换文件不原地改,权限照旧,见 replaceFile:
// 脚本可能正被 sh 读着)。返回开机自启开着没有。
// 顺带清掉旧版本改写时用的临时文件 p.tmp:改写被打断(断电、被杀)时留下的,名字以 S 开头、可执行,开机时 rc.unslung
// 会把它当成另一份启动脚本跑(半截的话就是跑一份坏脚本);脚本已经是当前模板时也清。
func refreshEntwareScript(p string) (enabled bool, err error) {
	_ = os.Remove(p + ".tmp")
	b, err := os.ReadFile(p)
	if err != nil {
		return false, err
	}
	want, enabled := entwareScriptLike(string(b))
	if string(b) == want {
		return enabled, nil
	}
	return enabled, replaceFile(p, []byte(want), modeOf(p, 0o755))
}

// mainUnitText 当前版本的服务单元。
func mainUnitText(k initKind, exe string) string {
	if k == initProcd {
		return fmt.Sprintf(procdScript, exe)
	}
	return fmt.Sprintf(systemdUnit, exe)
}

// refreshMainUnit 服务单元换成当前版本的内容:自更新只换程序文件,单元改了不会自己跟上。不重启服务,下次启动生效;
// ExecStopPost 这类 systemd 重载后就按新的来。
func refreshMainUnit(k initKind, exe string) error {
	want := mainUnitText(k, exe)
	if b, err := os.ReadFile(unitPath()); err == nil && string(b) == want {
		return nil
	}
	tmp := unitPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(want), 0o755); err != nil {
		return err
	}
	if k == initSystemd {
		_ = os.Chmod(tmp, 0o644)
	}
	if err := os.Rename(tmp, unitPath()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if k == initSystemd {
		_, err := sh("systemctl", "daemon-reload")
		return err
	}
	return nil
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

// Start 让初始化系统起服务;已经在跑就什么都不做。开机自启关着也照样起(那个开关只管开机)。
func Start() error {
	if err := needRoot(); err != nil {
		return err
	}
	ensureCurrentEntwareScript()
	return ctl(startAction(detect()))
}

// ensureCurrentEntwareScript 起停之前把已装的 Entware 脚本换成当前模板(开机自启的开关照抄)。手工换了程序、或面板升级后
// 守护进程没起来时,磁盘上还是旧版本写的脚本:它的起停交给 rc.func,按名字 pidof / killall godusevpn ——
// 正在调它的本进程(godusevpn start / install / stop / uninstall)在 start 时被当成"已经在跑"而不拉,在 stop 时被一起杀掉。
// 换不了也照样起停,结果和以前一样。
func ensureCurrentEntwareScript() {
	if detect() == initEntware && exists(unitPath()) {
		_, _ = refreshEntwareScript(unitPath())
	}
}

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
	// Entware 脚本的 stop 只按进程号停 "godusevpn run" 那个守护进程(就是发起重启的本进程),脚本进程不受影响;
	// 开机自启关着时脚本的 restart 照样重启正在跑的(就是本进程)。
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
	ensureCurrentEntwareScript()
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
		// 和启动脚本一样只认守护进程:只按名字 pidof 的话,正在跑 godusevpn status 的本进程自己就算一个,
		// 服务没在跑也报"在跑"。
		if len(entwareDaemons(name)) > 0 {
			return "running"
		}
		return "stopped"
	}
	return "stopped"
}

// entwareDaemons 名字叫 proc、命令行第二项是 run 的进程号 —— Entware 启动脚本拉起来的守护进程,和脚本里的 daemons
// 是同一个判断:名字照 rc.func 用 pidof 认,再只留命令行第二项是 run(脚本里的 ARGS)的。godusevpn status / start /
// install 这些命令也叫这个名字,只按名字认就把它们也算成服务。退出了、还没收尸的(僵尸)命令行是空的,不算。
func entwareDaemons(proc string) []int {
	out, err := exec.Command("pidof", proc).Output()
	if err != nil {
		return nil // 一个都没有(pidof 退出码 1),或者没有 pidof
	}
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(f)
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + f + "/cmdline")
		if err != nil {
			continue // 刚好退出了
		}
		if args := strings.Split(string(b), "\x00"); len(args) > 1 && args[1] == "run" {
			pids = append(pids, pid)
		}
	}
	return pids
}

func Status() string { return QueryStatus() }

// crashStderr 换掉 os.Stderr 之后,原来那个(fd 2,接着崩溃记录文件)得有人引用着:os.File 有终结器,没人引用了垃圾回收
// 就把 fd 2 关掉,运行时崩溃时就写不进去了(fd 2 还可能被别的文件、连接占去,崩溃输出写进别处)。
var crashStderr *os.File

// KeepStderrForCrashes 守护进程一启动就调(内核起来之前)。梅林(Entware)的启动脚本把标准错误接到 paths.EntwareStderrLog,
// 为的是留下运行时崩溃时直接往 fd 2 写的原因和栈;可守护进程平时也往标准错误写 —— 内核(sing-box)在 info 级给每条连接
// 记一行目标,写到它建日志那一刻的 os.Stderr,连接目标默认不落盘。所以标准错误接着这个文件时,把 Go 这一层写标准错误的
// (os.Stderr、标准库 log)改去 /dev/null,返回 true;运行时崩溃时照样直接写 fd 2,进那个文件。
// 别的情形(systemd / procd 收标准错误进系统日志、前台跑的接着终端)什么都不动。
func KeepStderrForCrashes() bool { return keepStderrForCrashes(paths.EntwareStderrLog) }

func keepStderrForCrashes(logPath string) bool {
	cur, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	want, err := os.Stat(logPath)
	if err != nil || !os.SameFile(cur, want) {
		return false
	}
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	crashStderr = os.Stderr
	os.Stderr = null
	log.SetOutput(null)
	return true
}

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
		return setEntwareEnabled(unitPath(), on)
	}
	return errors.New("没识别出初始化系统")
}

// setEntwareEnabled 改已装的 Entware 启动脚本 p 里开机自启的开关(ENABLED)。和 Refresh 一样换文件不原地改(见 replaceFile):
// 看门狗每分钟、起停时都有 sh 可能正读着这个脚本。
func setEntwareEnabled(p string, on bool) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	s := strings.ReplaceAll(strings.ReplaceAll(string(b), "ENABLED=yes", "ENABLED=no"), "ENABLED=no", map[bool]string{true: "ENABLED=yes", false: "ENABLED=no"}[on])
	if s == string(b) {
		return nil
	}
	return replaceFile(p, []byte(s), modeOf(p, 0o755))
}

// replaceFile 把 p 换成内容 data、权限 mode 的新文件:同目录先建一个临时文件写好、设好权限、落盘,再改名盖过去。
// 原地截断重写的话,正在读它的进程会读到空的、半截的内容 —— sh 跑脚本是边读边执行的(BusyBox 的 ash 一次只读一小段),
// 改名之后正在读的接着读旧的那份,新打开的读新的。临时文件名以 . 开头、带随机后缀:Entware 的 rc.unslung 开机时按 S*
// 找可执行的脚本挨个跑,改写被打断留下的临时文件不能被它当成另一份启动脚本;几个进程同时改写也各写各的,不会写进同一个文件。
func replaceFile(p string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, p)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// modeOf p 现在的权限位(用户改过的照旧,比如去掉执行位、不让 rc.unslung 开机时跑它);p 不在就用 def。
func modeOf(p string, def os.FileMode) os.FileMode {
	if st, err := os.Stat(p); err == nil {
		return st.Mode().Perm()
	}
	return def
}
