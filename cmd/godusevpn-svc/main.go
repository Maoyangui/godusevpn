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
	"strings"
	"syscall"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/daemon"
	"github.com/Maoyangui/godusevpn/internal/guardfix"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/netmode"
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
		// Resolve the current desktop/session owner independently of a UAC
		// administrator's token. Manual installs may explicitly supply an owner;
		// upgrades always preserve the already registered controller.
		owner := ""
		for _, arg := range os.Args[2:] {
			if strings.HasPrefix(arg, "--controller-user=") {
				owner = strings.Trim(strings.TrimPrefix(arg, "--controller-user="), "\"")
			}
		}
		if !ipc.ControllerOwnerRegistered() {
			var ownerErr error
			if owner != "" {
				ownerErr = ipc.RegisterControllerOwnerName(owner)
			} else {
				ownerErr = ipc.RegisterControllerOwner()
			}
			if ownerErr != nil {
				fail(fmt.Errorf("登记控制管道用户失败: %w", ownerErr))
			}
		}
		if err := svc.Install(exe); err != nil {
			fail(err)
		}
		if err := svc.Start(); err != nil {
			fail(fmt.Errorf("服务已注册但启动失败: %w", err))
		}
		fmt.Println("服务已安装并启动:", svc.DisplayName)
	case "uninstall":
		// 闸是持久的,卸载要撤掉,不然文件删了闸还在、机器一直断网。先停服务再撤,
		// 并且只有确认闸和 IPv6 都清理成功后才删除服务对象；失败时保留保护与可重试状态。
		if err := svc.Stop(); err != nil {
			fail(fmt.Errorf("停止服务以清理隐私保护失败: %w", err))
		}
		if err := netmode.ClearGuard(); err != nil {
			fail(fmt.Errorf("撤销全局禁直连失败,未执行卸载: %w", err))
		}
		if err := netmode.RestoreNICIPv6(); err != nil {
			fail(fmt.Errorf("还原网卡 IPv6 失败,未执行卸载: %w", err))
		}
		if err := svc.Uninstall(); err != nil {
			fail(err)
		}
		fmt.Println("服务已卸载")
	// guard clear:「恢复网络」——服务起不来、闸还在,手动把过滤器删掉。开始菜单的快捷方式、托盘菜单、卸载程序都走这里。
	case "guard":
		sub := ""
		if len(os.Args) > 2 {
			sub = os.Args[2]
		}
		switch sub {
		case "clear":
			if relaunchElevated() {
				return
			}
			text, ok := guardfix.Clear()
			fmt.Println(text)
			if len(os.Args) > 3 && os.Args[3] == "--popup" {
				notify(text)
			}
			if !ok {
				os.Exit(1)
			}
		case "status":
			n, err := guardfix.Status()
			if err != nil {
				fail(err)
			}
			if n == 0 {
				fmt.Println("闸:没开(直连不受限)")
			} else {
				fmt.Printf("闸:开着(%d 条过滤器;隧道以外的流量一律拦下)\n", n)
			}
		default:
			fmt.Println("用法: godusevpn-svc guard clear | status")
			os.Exit(2)
		}
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
	fmt.Println("用法: godusevpn-svc install | uninstall | start | stop | status | run | guard clear|status | version")
}
