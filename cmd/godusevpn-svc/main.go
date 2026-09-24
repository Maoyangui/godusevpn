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
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

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
	case "register-controller":
		// 把一个 Windows 账户加进控制管道的名单(需要管理员身份;主界面横幅上的「登记本账户」提权来调它)。
		// 带 --restart 时的退出码:0 = 登记并重启成功;1 = 登记本身失败;2 = 已登记但服务没停下来、没重启;
		// 3 = 已登记、服务已停但没拉起来(界面会再用普通用户权限试着拉一次)。
		// 不带参数就登记当前交互会话的用户;带参数可以是 SID 或账户名。管道的 ACL 在服务监听时算一次,
		// 所以要重启服务才生效;带 --restart 就顺手重启(闸是持久的、第二代不绑服务名,重启不撤闸)。
		var regErr error
		target, restart := "", false
		for _, a := range os.Args[2:] {
			if a == "--restart" {
				restart = true
			} else if target == "" {
				target = a
			}
		}
		switch {
		case strings.HasPrefix(target, "S-"):
			regErr = ipc.RegisterControllerOwnerSID(target)
		case target != "":
			regErr = ipc.RegisterControllerOwnerName(target)
		default:
			regErr = ipc.RegisterControllerOwner()
		}
		if regErr != nil {
			fail(fmt.Errorf("登记控制用户失败: %w", regErr))
		}
		fmt.Println("已登记。名单里现在有:", strings.Join(ipc.ControllerOwnerSIDs(), ", "))
		if !restart {
			fmt.Println("重启服务后生效:godusevpn-svc.exe stop && godusevpn-svc.exe start")
			return
		}
		// 0.7.4 在这里 Stop 一超时(守护进程收尾超过 20 秒)就直接失败退出,而停止请求已经发出去了 ——
		// 服务随后停下、却没人再把它拉起来,严格全局模式下整机断网,界面还报"已登记"。
		// 现在:停得慢就再等;停下来了就一定去拉起来;实在没停下来就不碰它、如实失败。
		stopped := svc.Stop() == nil
		for i := 0; !stopped && i < 60; i++ {
			time.Sleep(time.Second)
			stopped = svc.QueryStatus() == "stopped"
		}
		if !stopped {
			fmt.Fprintln(os.Stderr, "已登记,但服务迟迟没停下来,没能重启;重启电脑后生效")
			os.Exit(2)
		}
		if err := svc.Start(); err != nil && svc.QueryStatus() != "running" {
			fmt.Fprintln(os.Stderr, "已登记,服务已停止但启动失败(在开始菜单打开佛跳墙或重启电脑即可拉起):", err)
			os.Exit(3)
		}
		fmt.Println("服务已重启,登记生效")
	case "uninstall":
		// 闸是持久的,卸载要撤掉,不然文件删了闸还在、机器一直断网。先停服务再撤,
		// 并且只有确认闸和 IPv6 都清理成功后才删除服务对象；失败时保留保护与可重试状态。
		if err := svc.Stop(); err != nil {
			fail(fmt.Errorf("停止服务以清理隐私保护失败: %w", err))
		}
		if err := netmode.ClearGuard(); err != nil {
			fail(fmt.Errorf("撤销全局禁直连失败,未执行卸载: %w", err))
		}
		// 网卡 IPv6 没还原**不能**挡住卸载。它和闸不是一回事:闸还在等于机器断网、删掉工具就锁死,
		// 所以上面那道门必须守住;而网卡 IPv6 关着顶多是某几张网卡没有 v6,网照常能上。
		// m29 把这两件事同等对待,于是一张早就拔掉的 USB 网卡就能让产品永远卸不掉。
		// 这里改成:如实报出来、告诉用户怎么手动开回去,然后照常卸载。
		if err := netmode.RestoreNICIPv6(); err != nil {
			var inc *netmode.NICRestoreIncomplete
			if !errors.As(err, &inc) { // "原值丢了"那种下面从持久记录里说
				fmt.Println("注意:网卡 IPv6 没能还原回去:", err)
				fmt.Println("卸载继续。要手动开回去:在「网络适配器属性」里把「Internet 协议版本 6 (TCP/IPv6)」勾回来。")
			}
		}
		if lost := netmode.NICLossNote(); lost != "" {
			fmt.Println("注意:有几张网卡动手前的 IPv6 状态丢了,可能还关着:", lost)
			fmt.Println("要手动开回去:在「网络适配器属性」里把「Internet 协议版本 6 (TCP/IPv6)」勾回来。")
			_ = netmode.ClearNICLoss()
		}
		// 系统 DNS(macOS 被接管到隧道地址)/ 回包策略路由(Linux)和闸一样是持久的。m28 卸载时
		// 会经 stop() 无条件还原;m29 让 stop() 在"落盘仍写着想连"时保留密封,于是卸载之后 DNS
		// 永远指着一个已经不存在的隧道。卸载是用户明确要"回到没装过的样子",这里显式还原。
		if err := netmode.UnprotectChecked(); err != nil {
			fmt.Println("注意:系统 DNS / 路由没能还原:", err)
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
				if d := guardDetail(); d != "" {
					fmt.Println(d)
				}
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
