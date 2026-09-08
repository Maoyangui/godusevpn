// godusevpn 佛跳墙 的 Linux 客户端:一个静态二进制,既是守护进程(内核、订阅、控制口、Web 面板),也是命令行。
//
//	godusevpn run                       前台运行守护进程(初始化系统就是这样拉起它的)
//	godusevpn install | uninstall       注册 / 删除开机自启(systemd / procd / Entware),install 后随即启动
//	godusevpn start | stop | status
//	godusevpn passwd [新密码]            设置面板密码(不给参数就随机生成并打印)
//	godusevpn version
//	其它子命令(status / connect / mode / nodes / select / test / profile / rules / settings / logs / diag)见 internal/cli
package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/cli"
	"github.com/Maoyangui/godusevpn/internal/daemon"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/svc"
	"github.com/Maoyangui/godusevpn/internal/web"
)

func main() {
	cli.Name, cli.InstallHint = "godusevpn", "服务未运行:先执行 sudo godusevpn install(或 sudo godusevpn start)"
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "run":
		if os.Geteuid() != 0 {
			fail(fmt.Errorf("需要 root 权限(TUN 与路由)"))
		}
		if err := svc.Run(runDaemon); err != nil {
			fail(err)
		}
	case "install":
		exe, err := os.Executable()
		if err != nil {
			fail(err)
		}
		exe, _ = filepath.EvalSymlinks(exe)
		if err := paths.Ensure(); err != nil {
			fail(err)
		}
		s, _ := settings.Load(paths.Settings())
		pw := ""
		if s.WebPublic() && s.WebPassword == "" {
			pw = settings.RandomPassword()
			_ = s.SetWebPassword(pw)
		}
		if err := s.Save(paths.Settings()); err != nil {
			fail(err)
		}
		if err := svc.Install(exe); err != nil {
			fail(err)
		}
		if err := svc.Start(); err != nil {
			fail(fmt.Errorf("已注册自启但启动失败: %w", err))
		}
		fmt.Printf("%s 已安装并启动(%s)\n", svc.DisplayName, svc.Kind())
		printPanel(s, pw)
	case "uninstall":
		if err := svc.Uninstall(); err != nil {
			fail(err)
		}
		fmt.Println("已停止并删除自启;设置与数据保留在", paths.ConfDir(), "与", paths.DataDir())
	case "start":
		if err := svc.Start(); err != nil {
			fail(err)
		}
		fmt.Println("已启动")
	case "stop":
		if err := svc.Stop(); err != nil {
			fail(err)
		}
		fmt.Println("已停止")
	case "status":
		fmt.Println("服务:", svc.QueryStatus(), "("+svc.Kind()+")")
		code := cli.Main([]string{"status"})
		if s, err := settings.Load(paths.Settings()); err == nil {
			printPanel(s, "")
		}
		os.Exit(code)
	case "passwd":
		if os.Geteuid() != 0 {
			fail(fmt.Errorf("需要 root 权限"))
		}
		pw := ""
		if len(os.Args) > 2 {
			pw = os.Args[2]
		} else {
			pw = settings.RandomPassword()
		}
		if err := setPassword(pw); err != nil {
			fail(err)
		}
		fmt.Println("面板密码已设置为:", pw)
	case "version", "-v", "--version":
		fmt.Println(buildinfo.DisplayName, buildinfo.Version)
	case "", "help", "-h", "--help":
		usage()
	default:
		os.Exit(cli.Main(os.Args[1:]))
	}
}

// runDaemon 守护进程 + 面板一起跑。
func runDaemon(ctx context.Context) error {
	d, err := daemon.New()
	if err != nil {
		return err
	}
	ws := web.New(d)
	go func() {
		// 等控制口起来再读设置里的监听地址
		time.Sleep(300 * time.Millisecond)
		var s settings.Settings
		res, err := d.Dispatch(ipc.MGetSettings, nil)
		if err == nil {
			if v, ok := res.(settings.Settings); ok {
				s = v
			}
		}
		if s.WebListen == "" {
			d.Logf("面板未启用(设置 webListen 为空)")
			return
		}
		if err := ws.Serve(ctx, s.WebListen); err != nil {
			d.Logf("面板退出: %v", err)
		}
	}()
	return d.Run(ctx)
}

// setPassword 服务在跑就经控制口改(立即生效),没跑就直接改文件。
func setPassword(pw string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var s settings.Settings
	if err := ipc.Call(ctx, ipc.MGetSettings, nil, &s); err == nil {
		if err := s.SetWebPassword(pw); err != nil {
			return err
		}
		return ipc.Call(ctx, ipc.MSetSettings, s, &s)
	}
	if err := paths.Ensure(); err != nil {
		return err
	}
	s, _ = settings.Load(paths.Settings())
	if err := s.SetWebPassword(pw); err != nil {
		return err
	}
	return s.Save(paths.Settings())
}

// printPanel 装完 / 查状态时把面板地址完整打出来:监听 0.0.0.0 就把本机每个地址都列出来,监听回环就顺带提示怎么开放给局域网。
func printPanel(s settings.Settings, pw string) {
	if s.WebListen == "" {
		fmt.Println("面板: 未启用(godusevpn settings webListen=0.0.0.0:9800 开启)")
		return
	}
	host, port, err := net.SplitHostPort(s.WebListen)
	if err != nil {
		fmt.Println("面板: http://" + s.WebListen + "/")
		return
	}
	var urls []string
	if host == "" || host == "0.0.0.0" || host == "::" {
		for _, ip := range localIPv4() {
			urls = append(urls, "http://"+ip+":"+port+"/")
		}
		urls = append(urls, "http://127.0.0.1:"+port+"/")
	} else {
		urls = append(urls, "http://"+host+":"+port+"/")
	}
	fmt.Println("面板地址:")
	for _, u := range urls {
		fmt.Println("  " + u)
	}
	switch {
	case pw != "":
		fmt.Println("面板密码:", pw, "(请记下;改密码用 godusevpn passwd)")
	case s.WebPassword != "":
		fmt.Println("面板密码: 已设置(忘了可用 godusevpn passwd 重设)")
	}
	if !s.WebPublic() {
		fmt.Println("面板目前只允许本机访问;要在局域网其它设备上打开(路由器场景),执行:")
		fmt.Println("  godusevpn passwd            # 先设密码")
		fmt.Println("  godusevpn settings webListen=0.0.0.0:" + port)
	}
}

func localIPv4() []string {
	var out []string
	ifs, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 || i.Name == "godusevpn" || strings.HasPrefix(i.Name, "docker") || strings.HasPrefix(i.Name, "veth") {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && ipn.IP.IsGlobalUnicast() {
				out = append(out, ipn.IP.String())
			}
		}
	}
	return out
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "失败:", err)
	os.Exit(1)
}

func usage() {
	fmt.Println(buildinfo.DisplayName, buildinfo.Version)
	fmt.Println("用法: godusevpn run | install | uninstall | start | stop | status | passwd [密码] | version")
	cli.Usage()
}
