package core

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/ruleset"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 这条是 2026-09-18 那次事故的端到端闸门,走的是真实路径:
// 铺内置规则集 → 按默认设置生成配置 → 内核真的起一次。
//
// 事故本身:配置里的规则集全写成 type: remote,而 sing-box 在没有缓存时是在 box.Start() 里
// **同步下载**的,一条下不到整个内核就起不来。用户的索尼电视上表现为「内核启动失败,自动重试中」,
// 全局和规则模式都连不上,而且永远好不了 —— 每次重试都要重新去够一次 GitHub。
//
// 所以这里要证明的不是"配置长得对",而是**一台全新的机器、完全不联网,内核也能起来**。
// 测试里没有任何一处会去连网络:节点用的是 TEST-NET-1(192.0.2.0/24,RFC 5737 保留给文档用),
// 规则集全是本地文件,TUN 关掉,两个监听口都用系统分配的空闲端口。
func TestBuiltinRuleSetsBootTheCoreOffline(t *testing.T) {
	root := t.TempDir()
	if err := ruleset.Install(root); err != nil {
		t.Fatalf("铺内置规则集失败: %v", err)
	}

	s := settings.Default()
	s.LogLevel = "error" // 别让内核的 info 日志把 go test 的输出刷屏
	s.AdBlock = true     // 把第三个内置规则集也拉进来
	s.TUN = false        // 不碰这台机器的网络
	s.MixedPort = freePort(t)
	s.ClashPort = freePort(t)

	cfg, rep, err := builder.BuildEx(builder.Input{
		Profile:     testProfile(),
		Settings:    s,
		DataDir:     t.TempDir(),
		ClashSecret: "x",
		RuleSetDir:  root,
	})
	if err != nil {
		t.Fatalf("生成配置失败: %v", err)
	}
	if len(rep.Missing) != 0 {
		t.Fatalf("内置的三个都铺好了,不该报缺失: %v", rep.Missing)
	}

	// 1)配置里一条远程规则集都不能有,而且引用的本地文件必须真的在
	var parsed struct {
		Route struct {
			RuleSet []map[string]any `json:"rule_set"`
		} `json:"route"`
	}
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Route.RuleSet) != 3 {
		t.Fatalf("应该有三个内置规则集,实际 %d: %v", len(parsed.Route.RuleSet), parsed.Route.RuleSet)
	}
	for _, rs := range parsed.Route.RuleSet {
		if rs["type"] != "local" {
			t.Fatalf("规则集不是 local,内核启动会去联网下: %v", rs)
		}
		p, _ := rs["path"].(string)
		if st, err := os.Stat(p); err != nil || st.Size() == 0 {
			t.Fatalf("规则集 %v 指向的文件不存在或是空的: %v", rs["tag"], err)
		}
		if !strings.Contains(filepath.ToSlash(p), "/builtin/") {
			t.Fatalf("这一轮应该用内置那一层: %s", p)
		}
	}

	// 2)内核真起一次。这一步才是关键:Validate 只构造不启动,规则集是在启动阶段载入的,
	//    干跑通过不代表起得来 —— 事故那次 Validate 就是通过的。
	c := New(nil)
	if err := c.Validate(cfg); err != nil {
		t.Fatalf("干跑失败: %v", err)
	}
	if err := c.Start(cfg); err != nil {
		t.Fatalf("内核起不来(这正是用户电视上那个故障): %v", err)
	}
	if !c.Running() {
		t.Fatal("Start 返回成功,内核却没在跑")
	}
	if err := c.Stop(); err != nil {
		t.Fatalf("停不下来: %v", err)
	}
}

// 一个规则集都没有的机器(内置文件被杀毒软件删了、目录被清过)同样要能起来:
// 少几条分流规则可以,连不上不行。
func TestCoreBootsWithNoRuleSetsAtAll(t *testing.T) {
	s := settings.Default()
	s.LogLevel = "error" // 别让内核的 info 日志把 go test 的输出刷屏
	s.AdBlock = true
	s.TUN = false
	s.MixedPort = freePort(t)
	s.ClashPort = freePort(t)

	cfg, rep, err := builder.BuildEx(builder.Input{
		Profile: testProfile(), Settings: s, DataDir: t.TempDir(), ClashSecret: "x",
		RuleSetDir: t.TempDir(), // 空目录
	})
	if err != nil {
		t.Fatalf("生成配置失败: %v", err)
	}
	if len(rep.Missing) != 3 {
		t.Fatalf("三个都该报缺失,实际: %v", rep.Missing)
	}
	c := New(nil)
	if err := c.Start(cfg); err != nil {
		t.Fatalf("没有规则集也必须连得上,却起不来: %v", err)
	}
	_ = c.Stop()
}

// Android 上 Clash API 不监听端口(用不上,却是启动期硬依赖):去掉监听之后内核照样要起得来,
// 模式切换、选节点这些也还在(守护进程是直接从内核上下文里取 ClashServer 用的)。
//
// 这里 TUN 关着,所以混合入站会保留 —— 那是 TUN 关掉时唯一的入口,不能一起省掉。
// 「开着 TUN 的 Android 不生成混合入站」由 builder 那边的 TestAndroidHasNoLocalListeners 管:
// 那条只看生成出来的 JSON,不会真建 TUN。
func TestAndroidConfigBootsWithoutLocalListeners(t *testing.T) {
	root := t.TempDir()
	if err := ruleset.Install(root); err != nil {
		t.Fatal(err)
	}
	s := settings.Default()
	s.LogLevel = "error" // 别让内核的 info 日志把 go test 的输出刷屏
	s.TUN = false
	s.MixedPort = freePort(t) // 设置里填着,但 Android 分支不该把它写进配置
	s.ClashPort = freePort(t)

	cfg, _, err := builder.BuildEx(builder.Input{
		Profile: testProfile(), Settings: s, DataDir: t.TempDir(), ClashSecret: "x",
		RuleSetDir: root, Android: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Experimental struct {
			ClashAPI map[string]any `json:"clash_api"`
		} `json:"experimental"`
	}
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		t.Fatal(err)
	}
	if got := parsed.Experimental.ClashAPI["external_controller"]; got != "" {
		t.Fatalf("Android 上 Clash API 不该监听端口,实际 %q", got)
	}
	if parsed.Experimental.ClashAPI["default_mode"] == nil {
		t.Fatal("clash_api 整节被删了,模式切换会失效")
	}
	c := New(nil)
	if err := c.Start(cfg); err != nil {
		t.Fatalf("Android 那份配置起不来: %v", err)
	}
	_ = c.Stop()
}

// testProfile 两个节点,地址用 RFC 5737 的文档保留段,连出去也不会碰到真实主机。
func testProfile() *profile.Profile {
	return &profile.Profile{
		Tags: []string{"hk", "tw"},
		Outbounds: []json.RawMessage{
			json.RawMessage(`{"type":"hysteria2","tag":"hk","server":"192.0.2.1","server_port":443,"password":"p","tls":{"enabled":true,"server_name":"a.example"}}`),
			json.RawMessage(`{"type":"anytls","tag":"tw","server":"192.0.2.2","server_port":8443,"password":"p","tls":{"enabled":true,"server_name":"b.example"}}`),
		},
	}
}

// freePort 让系统分配一个当前没人用的端口,免得测试之间、以及和开发机上跑着的服务撞车。
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return p
}

// 反面对照:同一份配置,只把规则集换成"远程、地址还连不上",内核就整个起不来。
// 这正是用户电视上那个故障的机理,留在这里让上面那条"本地就能起来"有个参照,
// 也把"为什么绝不用 remote"钉成可执行的事实,而不是一句注释。
func TestRemoteRuleSetKillsTheCore(t *testing.T) {
	cfg := []byte(`{
  "log": {"level": "error"},
  "outbounds": [{"type": "direct", "tag": "direct"}],
  "route": {
    "rules": [{"rule_set": ["geosite-cn"], "outbound": "direct"}],
    "rule_set": [{"tag": "geosite-cn", "type": "remote", "format": "binary",
                  "url": "http://127.0.0.1:1/geosite-cn.srs", "download_detour": "direct"}],
    "final": "direct"
  }
}`)
	c := New(nil)
	if err := c.Validate(cfg); err != nil {
		t.Fatalf("干跑应该过 —— 构造阶段根本不碰规则集,这就是当初没被发现的原因: %v", err)
	}
	err := c.Start(cfg)
	if err == nil {
		_ = c.Stop()
		t.Fatal("远程规则集下不到,内核却起来了?那上面那条对照就没意义了")
	}
	if !strings.Contains(err.Error(), "rule-set") {
		t.Fatalf("失败原因应当指向规则集: %v", err)
	}
	t.Logf("内核拒绝启动,原话:%v", err)
}
