package daemon

import (
	"net"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/core"
	"github.com/Maoyangui/godusevpn/internal/logx"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// Clash API 的端口被别的程序占着(电视上装了别的 Clash 系应用,默认也是 9090)时,
// 不能让内核整个起不来,也不能干脆不监听 —— v0.6.23-m26 在 Android 上不监听,
// 连接页、网速、测延迟全坏了。正确做法是换一个空闲端口,并把实际端口报给界面。
func TestPickClashPort(t *testing.T) {
	d := newPortTestDaemon(t)

	t.Run("设置里那个空着就用它", func(t *testing.T) {
		want := freeLoopbackPort()
		if got := d.pickClashPort(want); got != want {
			t.Fatalf("端口空着却换了:想要 %d,给的 %d", want, got)
		}
	})

	t.Run("被别人占着就换一个真能用的", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0") // 扮演"别的 Clash 应用"
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		taken := l.Addr().(*net.TCPAddr).Port

		got := d.pickClashPort(taken)
		if got == taken {
			t.Fatalf("端口 %d 被占着,却照样用它 —— 内核会起不来", taken)
		}
		if !portFree(got) {
			t.Fatalf("换来的端口 %d 也用不了", got)
		}
	})

	t.Run("内核没在跑时界面按设置里的端口连", func(t *testing.T) {
		d.clashPort.Store(int32(freeLoopbackPort()))
		if got, want := d.activeClashPort(), d.getSettings().ClashPort; got != want {
			t.Fatalf("内核没在跑,却让界面去连 %d(应为设置里的 %d)", got, want)
		}
	})
}

func TestPortFree(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	if portFree(p) {
		t.Fatalf("端口 %d 明明被占着", p)
	}
	_ = l.Close()
	if !portFree(p) {
		t.Fatalf("端口 %d 已经放了,却说用不了", p)
	}
	for _, bad := range []int{0, -1, 70000} {
		if portFree(bad) {
			t.Fatalf("端口 %s 不合法,不该说能用", strconv.Itoa(bad))
		}
	}
}

func newPortTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := &Daemon{
		log:      logx.New(filepath.Join(t.TempDir(), "service.log"), 1<<20, 1),
		core:     core.New(nil),
		settings: settings.Default(),
	}
	// Windows 上开着的文件删不掉:不关的话 t.TempDir 的清理会失败(换端口时会写一行日志)
	t.Cleanup(func() { _ = d.log.Close() })
	return d
}
