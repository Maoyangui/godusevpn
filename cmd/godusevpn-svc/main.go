// godusevpn-svc 佛跳墙 的后台服务。
//
//	godusevpn-svc install     注册为 Windows 服务(自动启动)并启动
//	godusevpn-svc uninstall   停止并删除服务
//	godusevpn-svc start|stop  启停服务
//	godusevpn-svc run         前台运行(排障用,需要管理员权限的终端)
//	godusevpn-svc version
//
// 被服务管理器拉起时不带参数(或带 service),进入服务模式。
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/daemon"
	"github.com/Maoyangui/godusevpn/internal/svc"
)

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "", "service":
		if !svc.IsService() {
			usage()
			os.Exit(2)
		}
		if err := svc.Run(runDaemon); err != nil {
			fmt.Fprintln(os.Stderr, "服务运行失败:", err)
			os.Exit(1)
		}
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		fmt.Println(daemon.DisplayName, "前台运行中,Ctrl+C 停止")
		if err := runDaemon(ctx); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "install":
		exe, err := os.Executable()
		if err != nil {
			fail(err)
		}
		exe, _ = filepath.Abs(exe)
		if err := svc.Install(exe); err != nil {
			fail(err)
		}
		if err := svc.Start(); err != nil {
			fail(fmt.Errorf("服务已注册但启动失败: %w", err))
		}
		fmt.Println("服务已安装并启动:", svc.DisplayName)
	case "uninstall":
		if err := svc.Uninstall(); err != nil {
			fail(err)
		}
		fmt.Println("服务已卸载")
	case "start":
		if err := svc.Start(); err != nil {
			fail(err)
		}
		fmt.Println("服务已启动")
	case "stop":
		if err := svc.Stop(); err != nil {
			fail(err)
		}
		fmt.Println("服务已停止")
	case "status":
		fmt.Println(svc.Status())
	case "version", "-v", "--version":
		fmt.Println(daemon.DisplayName, buildinfo.Version)
	default:
		usage()
		os.Exit(2)
	}
}

func runDaemon(ctx context.Context) error {
	d, err := daemon.New()
	if err != nil {
		return err
	}
	return d.Run(ctx)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "失败:", err)
	os.Exit(1)
}

func usage() {
	fmt.Println(daemon.DisplayName, "服务", buildinfo.Version)
	fmt.Println("用法: godusevpn-svc install | uninstall | start | stop | status | run | version")
}
