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

const procdScript = `#!/bin/sh /etc/rc.common
# 佛跳墙 (godusevpn)
START=99
STOP=10
USE_PROCD=1

start_service() {
	procd_open_instance
	procd_set_param command %s run
	procd_set_param respawn 3600 5 5
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

. /opt/etc/init.d/rc.func
`

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
	switch detect() {
	case initSystemd:
		if err := os.WriteFile(unitPath(), []byte(fmt.Sprintf(systemdUnit, exe)), 0o644); err != nil {
			return err
		}
		if _, err := sh("systemctl", "daemon-reload"); err != nil {
			return err
		}
		_, err := sh("systemctl", "enable", name)
		return err
	case initProcd:
		if err := os.WriteFile(unitPath(), []byte(fmt.Sprintf(procdScript, exe)), 0o755); err != nil {
			return err
		}
		_, err := sh(unitPath(), "enable")
		return err
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
	switch detect() {
	case initSystemd:
		_, _ = sh("systemctl", "disable", name)
		_ = os.Remove(unitPath())
		_, _ = sh("systemctl", "daemon-reload")
	case initProcd:
		if exists(unitPath()) {
			_, _ = sh(unitPath(), "disable")
		}
		_ = os.Remove(unitPath())
	case initEntware:
		_ = os.Remove(unitPath())
		_ = os.Remove("/opt/bin/" + name)
	}
	return nil
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
func Stop() error  { return ctl("stop") }

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
