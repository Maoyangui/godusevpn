// godusevpn 佛跳墙 的托盘客户端:WebView2 主窗口 + 系统托盘,只做显示与控制,一切动作经命名管道交给后台服务。
//
//	godusevpn [--minimized] [godusevpn://import?url=…]
//	--minimized  只到托盘不弹窗(登录自启用)
//	godusevpn://import?url=…  落地页一键导入协议;已在运行时由第二个实例转交
package main

import (
	"os"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/web"
)

// 页面在仓库根的 web/dist,三端共用
var assets = web.Dist()

func parseArgs(args []string) (minimized bool, deepLink string) {
	for _, a := range args {
		switch {
		case a == "--minimized" || a == "-minimized":
			minimized = true
		case strings.HasPrefix(strings.ToLower(a), "godusevpn://"):
			deepLink = a
		}
	}
	return
}

func main() {
	minimized, link := parseArgs(os.Args[1:])
	app := newApp(minimized, link)
	err := wails.Run(&options.App{
		Title:             buildinfo.DisplayName,
		Width:             420, // 竖向小窗口
		Height:            760,
		MinWidth:          380,
		MinHeight:         640,
		MaxWidth:          560,
		MaxHeight:         1000,
		Frameless:         true, // 自绘标题栏
		HideWindowOnClose: true, // 点关闭只收到托盘,连接不受影响
		StartHidden:       minimized,
		BackgroundColour:  &options.RGBA{R: 244, G: 246, B: 250, A: 1},
		AssetServer:       &assetserver.Options{Assets: assets},
		OnStartup:         app.startup,
		OnDomReady:        app.domReady,
		OnShutdown:        app.shutdown,
		Bind:              []interface{}{app},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: "godusevpn-3c1f8d2e-7b4a-4e0c-9a6d-fotiaoqiang",
			OnSecondInstanceLaunch: func(d options.SecondInstanceData) {
				_, l := parseArgs(d.Args)
				app.secondInstance(l)
			},
		},
		Windows: &windows.Options{
			Theme:                             windows.SystemDefault,
			DisableFramelessWindowDecorations: false, // 保留系统阴影与圆角
		},
	})
	if err != nil {
		os.Exit(1)
	}
}
