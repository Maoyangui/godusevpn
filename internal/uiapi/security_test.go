package uiapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 安装包要下到只有守护进程能进的数据目录,不能下到 /tmp 这类人人可写的地方:Linux / macOS 上守护进程以 root
// 跑、装完以 root 运行新程序,别的本机用户抢建下载目录、下载期间换掉安装包,root 装上的就是他的程序。
func TestUpdateDirIsPrivate(t *testing.T) {
	data := t.TempDir()
	t.Setenv("GODUSEVPN_DATA", data)
	got := updateDir()
	if !strings.HasPrefix(got, data+string(filepath.Separator)) {
		t.Fatalf("下载目录 %s 不在数据目录 %s 里", got, data)
	}
	if strings.HasPrefix(got, filepath.Join(os.TempDir(), "godusevpn-update")) {
		t.Fatalf("下载目录还在共享的临时目录里: %s", got)
	}
}

type fakeBackend struct{}

func (fakeBackend) Dispatch(method string, _ json.RawMessage) (any, error) {
	if method == ipc.MGetState {
		return ipc.StateView{Settings: settings.Settings{WebPassword: "c2FsdA$deadbeef"}}, nil
	}
	return nil, nil
}
func (fakeBackend) Logf(string, ...any) {}

// 面板密码哈希不给页面:GetSettings 早就抹了,GetState 推的状态里也带着设置,同样要抹(状态每 1.5 秒推一次)。
func TestStateHidesWebPasswordHash(t *testing.T) {
	st := New(fakeBackend{}, Options{}).State()
	if st.View.Settings.WebPassword != "" {
		t.Fatalf("状态里带着面板密码哈希: %q", st.View.Settings.WebPassword)
	}
}
