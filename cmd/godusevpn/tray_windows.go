package main

import (
	_ "embed"
	"sync"

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

	show, conn, upgrade, rule, global, direct, quitItem *systray.MenuItem
}

func newTray(a *App) *trayUI { return &trayUI{app: a} }

var trayText = map[string]map[string]string{
	"zh": {"show": "显示主窗口", "connect": "连接", "disconnect": "断开", "mode": "模式", "rule": "规则", "global": "全局", "direct": "直连", "quit": "退出", "svcdown": "服务未运行", "upgrade": "升级到"},
	"en": {"show": "Show window", "connect": "Connect", "disconnect": "Disconnect", "mode": "Mode", "rule": "Rule", "global": "Global", "direct": "Direct", "quit": "Quit", "svcdown": "Service not running", "upgrade": "Update to"},
}

func (t *trayUI) tr(key string) string {
	m := trayText[t.app.GetState().Lang]
	if m == nil {
		m = trayText["zh"]
	}
	return m[key]
}

func (t *trayUI) run() {
	systray.Run(t.onReady, nil)
}

func (t *trayUI) onReady() {
	systray.SetIcon(iconOff)
	systray.SetTooltip(buildinfo.DisplayName)
	systray.SetOnClick(func(systray.IMenu) { t.app.toggleWindow() })
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
	t.quitItem = systray.AddMenuItem(t.tr("quit"), "")

	t.show.Click(func() { t.app.showWindow() })
	t.conn.Click(func() {
		st := t.app.GetState()
		if st.View.State.Wanted {
			_, _ = t.app.Disconnect()
		} else {
			_, _ = t.app.Connect()
		}
	})
	t.rule.Click(func() { _, _ = t.app.SetMode("rule") })
	t.global.Click(func() { _, _ = t.app.SetMode("global") })
	t.direct.Click(func() { _, _ = t.app.SetMode("direct") })
	t.quitItem.Click(func() { t.app.QuitApp() })
	t.upgrade.Click(func() { t.app.showAbout() })
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
