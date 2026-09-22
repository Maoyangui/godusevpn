package main

import (
	_ "embed"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"os"
	"path/filepath"
	gort "runtime"
	"sync"
	"sync/atomic"

	"github.com/energye/systray"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
)

//go:embed build/tray/on.ico
var iconOn []byte

//go:embed build/tray/off.ico
var iconOff []byte

//go:embed build/tray/err.ico
var iconErr []byte

// trayUI 系统托盘:图标随状态变色,右键菜单可连接 / 断开、切模式、显示窗口、退出。
type trayUI struct {
	app   *App
	mu    sync.Mutex
	ready bool
	last  string // 上次设置的图标状态,避免每 1.5 秒重设一次

	show, conn, upgrade, rule, global, direct, guardFix, quitItem *systray.MenuItem
}

func newTray(a *App) *trayUI { return &trayUI{app: a} }

var trayText = map[string]map[string]string{
	"zh": {"show": "显示主窗口", "connect": "连接", "disconnect": "断开", "mode": "模式", "rule": "规则", "global": "全局", "direct": "直连", "quit": "退出", "svcdown": "服务未运行", "upgrade": "升级到", "guardfix": "恢复网络(解除禁直连闸)"},
	"en": {"show": "Show window", "connect": "Connect", "disconnect": "Disconnect", "mode": "Mode", "rule": "Rule", "global": "Global", "direct": "Direct", "quit": "Quit", "svcdown": "Service not running", "upgrade": "Update to", "guardfix": "Restore network (lift no-direct guard)"},
}

func (t *trayUI) tr(key string) string {
	m := trayText[t.app.GetState().Lang]
	if m == nil {
		m = trayText["zh"]
	}
	return m[key]
}

func (t *trayUI) run() {
	// Windows 的消息队列是按线程分的:systray 在哪个 OS 线程上建出托盘窗口,之后就必须一直在
	// 同一个线程上 GetMessage。这个 goroutine 不钉死在一条线程上的话,Go 调度器会在 GetMessage
	// 阻塞、返回的间隙把它挪到别的 M 上 —— 托盘窗口的消息队列从此没人取:图标还在、还会随状态
	// 变色(那是 Shell_NotifyIcon 直接画的,不经消息循环),左右键却全部失灵,而且再也好不了。
	// systray 包自己 init() 里的 LockOSThread 锁的是 main goroutine(被 Wails 占着),帮不到这里。
	// 这里的 gort 是标准库 runtime —— 本包里 app.go 的 runtime 是 Wails 的那个,别混。
	// 不配 Unlock:这个 goroutine 活多久,这条线程就归它多久。
	gort.LockOSThread()
	systray.Run(t.onReady, nil)
}

// async 把点击处理挪到单独的 goroutine 里跑,并且同一项在途时忽略重复点击。
//
// 挪出去:systray 是在窗口过程里**同步**调这些回调的(systray_windows.go 的 wndProc),回调阻塞多久,
// 托盘就多久不处理任何消息。而这里的回调随手就能阻塞很久 —— IPC 最长等 90 秒(App.call),
// 「恢复网络」还要等用户点 UAC。所以一律立刻还给 wndProc,让消息循环继续转。
//
// 去重:挪出去之后就没有 wndProc 天然的串行了,连点会叠出几个 UAC 提权框、发两次连接请求,
// 「退出」一边停服务一边还能点「连接」、把落盘的连接意愿写回"想连"。每次调 async 各自带一个
// 在途标志,所以各菜单项互不影响:慢的那项不会挡住别的项。
func async(fn func()) func() {
	var busy atomic.Bool
	return func() {
		if !busy.CompareAndSwap(false, true) {
			return // 这一项还在跑,重复点击直接丢掉(不排队:用户要的是"点一下"而不是"点几下做几遍")
		}
		go func() {
			defer busy.Store(false)
			fn()
		}()
	}
}

func (t *trayUI) onReady() {
	systray.SetIcon(iconOff)
	systray.SetTooltip(buildinfo.DisplayName)
	// 左键单击:和菜单项走同一套(异步 + 在途去重)。传进来的 IMenu 故意不用 —— 它的 ShowMenu
	// 只能在泵线程里调,挪到 goroutine 里调会静默失败。
	showMain := async(t.app.toggleWindow)
	systray.SetOnClick(func(systray.IMenu) { showMain() })
	t.show = systray.AddMenuItem(t.tr("show"), "")
	t.conn = systray.AddMenuItem(t.tr("connect"), "")
	t.upgrade = systray.AddMenuItem("", "")
	t.upgrade.Hide()
	systray.AddSeparator()
	mode := systray.AddMenuItem(t.tr("mode"), "")
	t.rule = mode.AddSubMenuItemCheckbox(t.tr("rule"), "", true)
	t.global = mode.AddSubMenuItemCheckbox(t.tr("global"), "", false)
	t.direct = mode.AddSubMenuItemCheckbox(t.tr("direct"), "", false)
	systray.AddSeparator()
	t.guardFix = systray.AddMenuItem(t.tr("guardfix"), "")
	t.quitItem = systray.AddMenuItem(t.tr("quit"), "")

	t.show.Click(async(func() { t.app.showWindow() }))
	t.conn.Click(async(func() {
		st := t.app.GetState()
		if st.View.State.Wanted {
			_, _ = t.app.Disconnect()
		} else {
			_, _ = t.app.Connect()
		}
	}))
	t.rule.Click(async(func() { _, _ = t.app.SetMode("rule") }))
	t.global.Click(async(func() { _, _ = t.app.SetMode("global") }))
	t.direct.Click(async(func() { _, _ = t.app.SetMode("direct") }))
	t.quitItem.Click(async(func() { t.app.QuitApp() }))
	// 恢复网络:服务活着就关掉「全局禁直连」的开关(闸随之撤);服务不在就提权跑恢复命令,直接删过滤器
	t.guardFix.Click(async(func() {
		if t.app.GetState().Service {
			var v ipc.StateView
			if err := t.app.call(ipc.MGetState, nil, &v); err == nil && v.Guard == "" && v.GuardError == "" {
				return // 闸本来就没开,没什么可解除的;别顺手把「全局禁直连」开关关了
			}
			if s, err := t.app.GetSettings(); err == nil {
				s.NoDirect = false
				_, _ = t.app.SaveSettings(s)
			}
			return
		}
		exe, err := os.Executable()
		if err != nil {
			return
		}
		if p, err := serviceBinary(filepath.Dir(exe)); err == nil {
			_ = runElevated(p, "guard clear --popup")
		}
	}))
	t.upgrade.Click(async(func() { t.app.showAbout() }))
	t.mu.Lock()
	t.ready = true
	t.mu.Unlock()
	t.update(t.app.GetState())
}

// update 按最新状态刷图标、提示与菜单文字。
func (t *trayUI) update(st UIState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.ready {
		return
	}
	status := "off"
	tip := buildinfo.DisplayName
	switch {
	case !st.Service:
		status = "err"
		tip += " · " + t.tr("svcdown")
	case st.View.State.Status == "connected":
		status = "on"
		tip += " · " + st.View.Node
	case st.View.State.Status == "degraded", st.View.State.Status == "error":
		status = "err"
		tip += " · " + st.View.State.Error
	case st.View.State.Wanted:
		status = "err"
		tip += " · " + string(st.View.State.Status)
	}
	if status != t.last {
		switch status {
		case "on":
			systray.SetIcon(iconOn)
		case "err":
			systray.SetIcon(iconErr)
		default:
			systray.SetIcon(iconOff)
		}
		t.last = status
	}
	if len(tip) > 120 {
		tip = tip[:120]
	}
	systray.SetTooltip(tip)
	if st.View.State.Wanted {
		t.conn.SetTitle(t.tr("disconnect"))
	} else {
		t.conn.SetTitle(t.tr("connect"))
	}
	if st.Service {
		t.conn.Enable()
	} else {
		t.conn.Disable()
	}
	if st.Update != nil {
		t.upgrade.SetTitle(t.tr("upgrade") + " v" + st.Update.Version)
		t.upgrade.Show()
	} else {
		t.upgrade.Hide()
	}
	set := func(item *systray.MenuItem, on bool) {
		if on {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
	set(t.rule, st.View.Mode == "rule")
	set(t.global, st.View.Mode == "global")
	set(t.direct, st.View.Mode == "direct")
}

// relabel 切换语言后重写菜单文字。
func (t *trayUI) relabel() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.ready {
		return
	}
	t.show.SetTitle(t.tr("show"))
	t.rule.SetTitle(t.tr("rule"))
	t.global.SetTitle(t.tr("global"))
	t.direct.SetTitle(t.tr("direct"))
	t.quitItem.SetTitle(t.tr("quit"))
}

func (t *trayUI) quit() {
	t.mu.Lock()
	ready := t.ready
	t.mu.Unlock()
	if ready {
		systray.Quit()
	}
}
