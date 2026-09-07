// godusevpn 佛跳墙 的托盘客户端:WebView2 主窗口 + 系统托盘,只做显示与控制,一切动作经命名管道交给后台服务。
//
//	godusevpn [--minimized]   --minimized:只到托盘不弹窗(登录自启用)
package main

import (
	"embed"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	minimized := false
	for _, a := range os.Args[1:] {
		if a == "--minimized" || a == "-minimized" {
			minimized = true
		}
	}
	app := newApp(minimized)
	err := wails.Run(&options.App{
		Title:             buildinfo.DisplayName,
		Width:             980,
		Height:            660,
		MinWidth:          820,
		MinHeight:         560,
		HideWindowOnClose: true, // 点关闭只收到托盘,连接不受影响
		StartHidden:       minimized,
		BackgroundColour:  &options.RGBA{R: 246, G: 247, B: 250, A: 1},
		AssetServer:       &assetserver.Options{Assets: assets},
		OnStartup:         app.startup,
		OnShutdown:        app.shutdown,
		Bind:              []interface{}{app},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "godusevpn-3c1f8d2e-7b4a-4e0c-9a6d-fotiaoqiang",
			OnSecondInstanceLaunch: func(options.SecondInstanceData) { app.showWindow() },
		},
		Windows: &windows.Options{Theme: windows.SystemDefault},
	})
	if err != nil {
		os.Exit(1)
	}
}
