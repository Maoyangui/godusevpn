// macOS 这一版不做菜单栏图标。
//
// 原因是 Wails 与 systray 都要占着 Cocoa 的主线程,凑到一起要走"外部事件循环"那条路,
// 而菜单栏图标的样子(模板图标、点击行为)不在真机上盯着看是调不准的 —— 与其塞一个没把握的东西,
// 不如先按 macOS 的常规来:程序有 Dock 图标和窗口,关掉窗口只是关界面,隧道在后台服务里照常跑,
// 从启动台再打开就回来了。菜单栏图标留到有真机能盯着调的时候再加。
package main

type trayUI struct{}

func newTray(*App) *trayUI { return &trayUI{} }

func (t *trayUI) run()           {}
func (t *trayUI) quit()          {}
func (t *trayUI) update(UIState) {}
func (t *trayUI) relabel()       {}
