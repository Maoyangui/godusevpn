// Package web 三端共用的面板页面(竖版界面):Windows 的 WebView2 窗口、Linux 守护进程内置的 HTTP 面板、
// Android 的 WebView 都加载这里的 dist/。页面通过 window.go.main.App.* 调后端,Windows 由 Wails 注入,
// 浏览器环境由 dist/api.js 用 HTTP + SSE 模拟同一套接口。
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist 以 index.html 为根的文件系统。
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
