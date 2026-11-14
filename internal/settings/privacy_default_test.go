package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// privacyDefaults 出厂就开着的隐私项,README「默认策略」写的就是这些:改了出厂值、或者哪次迁移把老用户的翻了,
// 这里就失败 —— 要改先想清楚,再同步改 README 和发布说明。
var privacyDefaults = []struct {
	name string
	get  func(Settings) bool
	want bool
}{
	{"noDirect(防流量逃逸)", func(s Settings) bool { return s.NoDirect }, true},
	{"ipv6(允许 IPv6)", func(s Settings) bool { return s.IPv6 }, false},
	{"disableNicIpv6(连接时停用网卡 IPv6)", func(s Settings) bool { return s.DisableNICIPv6 }, true},
	{"tun(TUN 全机接管)", func(s Settings) bool { return s.TUN }, true},
	{"strictRoute(严格路由)", func(s Settings) bool { return s.StrictRoute }, true},
	{"fakeIp", func(s Settings) bool { return s.FakeIP }, true},
	{"coreLogDisk(内核日志写全到磁盘)", func(s Settings) bool { return s.CoreLogDisk }, false},
	{"webAllowPublic(允许从公网打开面板)", func(s Settings) bool { return s.WebAllowPublic }, false},
}

func checkPrivacyDefaults(t *testing.T, where string, s Settings) {
	t.Helper()
	for _, c := range privacyDefaults {
		if got := c.get(s); got != c.want {
			t.Errorf("%s:%s 应为 %v,得 %v", where, c.name, c.want, got)
		}
	}
}

// 出厂值,以及老设置文件(缺这些键、或者 schema 早于这些项加进来的那一版)读进来以后的值。
// 用户自己在当前 schema 下关掉的,读进来照样是关:迁移只补缺的,不替人翻已经写明的选择。
func TestPrivacyDefaultsPinned(t *testing.T) {
	checkPrivacyDefaults(t, "Default()", Default())
	if Default().RemoteDNS != "1.1.1.1" {
		t.Errorf("Default():远程 DNS 应是经代理的 1.1.1.1,得 %q", Default().RemoteDNS)
	}

	dir := t.TempDir()
	load := func(name, body string) Settings {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := Load(p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return s
	}
	// 当前 schema、什么键都没写:全按出厂值
	checkPrivacyDefaults(t, "只写了 schema 的设置文件", load("bare.json", `{"schema":4}`))
	// 早于「停用网卡 IPv6」「防流量逃逸」的老文件,两项在文件里明写着 false(老版本落盘时就是这样):迁移按新默认补成开
	checkPrivacyDefaults(t, "schema 2 的老文件", load("s2.json", `{"schema":2,"mode":"rule","tun":true,"strictRoute":true,"fakeIp":true,"ipv6":false,"disableNicIpv6":false,"noDirect":false}`))
	checkPrivacyDefaults(t, "schema 3 的老文件", load("s3.json", `{"schema":3,"mode":"rule","tun":true,"strictRoute":true,"fakeIp":true,"ipv6":false,"disableNicIpv6":true,"noDirect":false}`))
	checkPrivacyDefaults(t, "schema 1 的老文件", load("s1.json", `{"schema":1,"profileUrl":"https://x.example/sub/u","mode":"rule","tun":true}`))

	// 出厂值存一次再读回来,一项都不变
	p := filepath.Join(dir, "saved.json")
	if err := Default().Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	checkPrivacyDefaults(t, "出厂值存盘再读回", got)

	// 当前 schema 下用户自己关掉的,读回来照旧是关
	off := Default()
	off.NoDirect, off.DisableNICIPv6, off.IPv6 = false, false, true
	b, _ := json.Marshal(off)
	mine := load("mine.json", string(b))
	if mine.NoDirect || mine.DisableNICIPv6 || !mine.IPv6 {
		t.Fatalf("用户自己改过的不该被迁移翻回去: noDirect=%v disableNicIpv6=%v ipv6=%v", mine.NoDirect, mine.DisableNICIPv6, mine.IPv6)
	}
}
