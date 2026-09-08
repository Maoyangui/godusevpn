//go:build linux || android

// Package mobile Android 用的引擎,gomobile 编成 AAR。Kotlin 侧只管 VpnService、通知栏、WebView;
// 守护进程(设置、订阅与刷新回退链、配置生成、状态机、规则组、测速)和页面方法集与 Windows / Linux 是同一份代码。
//
//	engine, err := mobile.NewEngine(filesDir, host, listener)
//	engine.Call("Connect", "[]")            // 页面调的每个方法都可以这样调,参数是 JSON 数组
//	engine.Close()
package mobile

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/daemon"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/uiapi"
)

// EventListener 状态 / 速度 / 更新进度事件,data 是 JSON。
type EventListener interface {
	OnEvent(name string, dataJSON string)
}

// Engine 一个进程一个。
type Engine struct {
	d      *daemon.Daemon
	ui     *uiapi.Service
	cancel context.CancelFunc
	done   chan struct{}
}

// NewEngine dataDir 是应用私有目录(设置、缓存、日志都放这里),cacheDir 是应用缓存目录(升级包下载到这里,由 FileProvider 交给系统安装器),
// host 是宿主实现,listener 收事件(可为 nil)。
func NewEngine(dataDir, cacheDir string, host Host, listener EventListener) (*Engine, error) {
	_ = os.Setenv("GODUSEVPN_CONF", filepath.Join(dataDir, "conf"))
	_ = os.Setenv("GODUSEVPN_DATA", filepath.Join(dataDir, "data"))
	if cacheDir != "" {
		_ = os.Setenv("TMPDIR", cacheDir) // Android 上 os.TempDir() 默认是 /data/local/tmp,应用写不了
	}
	// Go 侧崩溃(panic / fatal)默认只进 logcat,真机拿不到;另写一份到 logs/crash.log,诊断包会带上
	logDir := filepath.Join(dataDir, "data", "logs")
	if err := os.MkdirAll(logDir, 0o700); err == nil {
		if f, err := os.OpenFile(filepath.Join(logDir, "crash.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			_ = debug.SetCrashOutput(f, debug.CrashOptions{})
		}
	}
	d, err := daemon.NewWithOptions(daemon.Options{Platform: newPlatform(host), NoListen: true})
	if err != nil {
		return nil, err
	}
	ui := uiapi.New(d, uiapi.Options{
		Platform:  "android",
		PrefsPath: filepath.Join(dataDir, "conf", "ui.json"),
		InstallUpdate: func(path string) error { // 下载好 APK 交给宿主装
			host.Log("INFO", "update downloaded: "+path)
			listener.OnEvent("install-update", jsonString(path))
			return nil
		},
	})
	e := &Engine{d: d, ui: ui, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	// Android 没有面板监听,也没有可视化以外的入口,把面板关掉免得设置校验拦着
	if s := ui.Settings(); s.WebListen != "" {
		s.WebListen = ""
		_, _ = d.Dispatch(ipc.MSetSettings, mustJSON(s))
	}
	go func() {
		defer close(e.done)
		_ = d.Run(ctx)
	}()
	go ui.Run(ctx)
	if listener != nil {
		go func() {
			ch, cancelSub := ui.Subscribe()
			defer cancelSub()
			for {
				select {
				case <-ctx.Done():
					return
				case ev := <-ch:
					b, err := json.Marshal(ev.Data)
					if err == nil {
						listener.OnEvent(ev.Name, string(b))
					}
				}
			}
		}()
	}
	return e, nil
}

// Call 页面方法:名字与 web/dist 里调的一致,参数是 JSON 数组,返回结果 JSON。
func (e *Engine) Call(name, argsJSON string) (string, error) {
	return e.ui.CallJSON(name, argsJSON)
}

// State 当前状态 JSON(给通知栏、磁贴用)。
func (e *Engine) State() string {
	b, _ := json.Marshal(e.ui.State())
	return string(b)
}

// Close 断开并停掉守护进程;等它收尾。
func (e *Engine) Close() {
	e.cancel()
	select {
	case <-e.done:
	case <-time.After(5 * time.Second):
	}
}

// Version 引擎版本。
func Version() string { return buildinfo.Version }

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
