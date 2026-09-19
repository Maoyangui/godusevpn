package daemon

import (
	"net"
	"strconv"
)

// Clash API 的监听端口。
//
// 这个端口各端都得开:界面服务层(internal/uiapi,Android 与 Linux 面板共用)的实时网速、连接列表、
// 断开连接、连着时测延迟都是经它取的;Windows 托盘客户端也一样。
// 但它是**启动期硬依赖** —— 端口被别的程序占着(电视上常见:装了别的 Clash 系应用,默认也是 9090),
// 内核整个起不来,而电视界面里连改端口的地方都没有。
//
// v0.6.23-m26 的做法是 Android 上干脆不监听,结果上面那些功能全坏了(连接页 connection refused、网速恒 0)。
// 现在改成:设置里那个端口空着就用它;被别人占着就换一个空闲的,并把实际端口经 GetClashInfo 报给界面 ——
// 界面那边的网速流按「端口:密钥」识别连接,端口一变会自己重连。

// pickClashPort 这一轮内核该监听哪个端口。
func (d *Daemon) pickClashPort(want int) int {
	cur := int(d.clashPort.Load())
	running := d.core.Running()
	// 正在跑的内核自己占着想要的那个:停旧起新时它会先空出来,照用
	if running && cur == want {
		return want
	}
	if portFree(want) {
		return want
	}
	// 想要的被别人占着。内核在跑就沿用它现在这个 —— 停旧起新时同样会先空出来,端口不用来回换
	if running && cur > 0 {
		return cur
	}
	if p := freeLoopbackPort(); p > 0 {
		d.logf("Clash API 端口 %d 被别的程序占着,这次改用 %d(界面会跟着走;外部面板要连的话以日志里这个端口为准)", want, p)
		return p
	}
	return want // 连空闲端口都拿不到:照原样写,起不来时错误归类会说清楚是端口的事
}

// activeClashPort 界面该连哪个端口:内核跑着就是它真正监听的那个,否则是设置里的。
func (d *Daemon) activeClashPort() int {
	if p := int(d.clashPort.Load()); p > 0 && d.core.Running() {
		return p
	}
	return d.getSettings().ClashPort
}

func (d *Daemon) preparedClashPortValue() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.preparedClashPort > 0 {
		return d.preparedClashPort
	}
	return d.settings.ClashPort
}

// portFree 这个回环端口此刻能不能监听。
func portFree(port int) bool {
	if port <= 0 || port > 65535 {
		return false
	}
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// freeLoopbackPort 让系统分一个当前空闲的回环端口。
func freeLoopbackPort() int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
