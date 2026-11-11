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

const (
	pwHash   = "c2FsdA$deadbeefcafe"                  // 面板密码的加盐哈希
	subToken = "https://panel.example/sub/TOKEN-9f3a" // 带令牌的订阅地址
	renewURL = "https://panel.example/renew?u=TOKEN-9f3a"
)

// secretBackend 服务给的每样东西都带着凭据(状态里的设置、订阅;设置本身),记下页面方法最后存回去的设置。
type secretBackend struct{ saved settings.Settings }

func secretSettings() settings.Settings {
	s := settings.Default()
	s.WebPassword = pwHash
	s.Profiles = []settings.Profile{{ID: "p1", Name: "一", URL: subToken}}
	s.ActiveProfile = "p1"
	return s
}

func secretView() ipc.StateView {
	pv := ipc.ProfileView{ID: "p1", Name: "一", URL: subToken, WebPage: renewURL, Active: true}
	return ipc.StateView{Node: "香港1", Settings: secretSettings(), Profile: &pv, Profiles: []ipc.ProfileView{pv}}
}

func (b *secretBackend) Dispatch(method string, params json.RawMessage) (any, error) {
	switch method {
	case ipc.MGetSettings:
		return secretSettings(), nil
	case ipc.MSetSettings:
		_ = json.Unmarshal(params, &b.saved)
		return b.saved, nil
	case ipc.MGetClashInfo:
		return ipc.ClashInfo{}, nil
	}
	return secretView(), nil // 连接、断开、切模式、选节点、线路自动、切订阅、取状态:都回整份状态
}
func (*secretBackend) Logf(string, ...any) {}

// 经网页面板(和 Android 的页面桥)交给页面的状态、设置里一律不带凭据:面板密码哈希(单轮加盐 SHA-256,拿到能离线爆破)、
// 带令牌的订阅地址、续费地址。状态每 1.5 秒推一次,连接、切模式、选节点、线路自动、切订阅的返回值也是整份状态 ——
// 局域网明文访问时路上都看得见。早先只有 GetState 和 GetSettings 抹了哈希,别的几条照原样带着。
func TestPageNeverSeesCredentials(t *testing.T) {
	b := &secretBackend{}
	svc := New(b, Options{})
	check := func(what string, res any) {
		t.Helper()
		raw, err := json.Marshal(res)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{pwHash, "TOKEN-9f3a"} {
			if strings.Contains(string(raw), secret) {
				t.Fatalf("%s 交给页面的结果里带着凭据 %q: %s", what, secret, raw)
			}
		}
	}
	check("状态推送", svc.State())
	for _, m := range []struct {
		name string
		args []json.RawMessage
	}{
		{"GetState", nil}, {"Connect", nil}, {"Disconnect", nil},
		{"SetMode", []json.RawMessage{json.RawMessage(`"global"`)}},
		{"SelectNode", []json.RawMessage{json.RawMessage(`"香港1"`)}},
		{"SetLineAuto", []json.RawMessage{json.RawMessage(`true`)}},
		{"SelectProfile", []json.RawMessage{json.RawMessage(`"p1"`)}},
		{"GetSettings", nil},
	} {
		res, err := svc.Call(m.name, m.args)
		if err != nil {
			t.Fatalf("%s: %v", m.name, err)
		}
		check(m.name, res)
	}
	// 设置页拿到的是去掉哈希的那份、原样存回来:不能因此把面板密码清掉(服务那边要收到原来的哈希),返回给页面的照样不带
	page, _ := svc.Call("GetSettings", nil)
	in, _ := json.Marshal(page)
	res, err := svc.Call("SaveSettings", []json.RawMessage{in})
	if err != nil {
		t.Fatal(err)
	}
	check("SaveSettings", res)
	if b.saved.WebPassword != pwHash {
		t.Fatalf("保存设置把面板密码弄丢了: %q", b.saved.WebPassword)
	}
}
