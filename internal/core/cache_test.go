package core

import (
	"strings"
	"testing"
)

// cacheCfg 和 builder 同样的结构:proxy 选择组(default 可设)+ auto 测速组 + clash_mode 分支 + cache_file。
// 节点都是 direct,测速地址是本机一个关着的端口:没有入站、不碰路由,也不往外发包。
func cacheCfg(cache, def, mode string) []byte {
	return []byte(`{"log":{"level":"error"},
"outbounds":[
{"type":"selector","tag":"proxy","outbounds":["auto","hk1","tw1"],"default":"` + def + `"},
{"type":"urltest","tag":"auto","outbounds":["hk1","tw1"],"url":"http://127.0.0.1:9/","interval":"24h","idle_timeout":"25h"},
{"type":"direct","tag":"hk1"},{"type":"direct","tag":"tw1"},{"type":"direct","tag":"direct"}],
"route":{"rules":[{"clash_mode":"Direct","outbound":"direct"},{"clash_mode":"Global","outbound":"proxy"}],"final":"proxy"},
"dns":{"rules":[{"clash_mode":"Rule","server":"local"}],"servers":[{"type":"local","tag":"local"}]},
"experimental":{"clash_api":{"default_mode":"` + mode + `"},"cache_file":{"enabled":true,"path":"` + cache + `"}}}`)
}

func startCache(t *testing.T, cache, def, mode string) *Core {
	t.Helper()
	c := New(nil)
	if err := c.Start(cacheCfg(cache, def, mode)); err != nil {
		t.Fatal(err)
	}
	return c
}

func proxyNow(t *testing.T, c *Core) string {
	t.Helper()
	now, _, err := c.Group("proxy")
	if err != nil {
		t.Fatal(err)
	}
	return now
}

// 缓存里记着的模式与选中节点会盖过配置:内核启动前必须按这一轮配置改掉。
// 场景:连着时手动选过 hk1、老版本切模式存过 Direct;之后设置改成严格全局 + 自动选择,重新连接。
func TestPresetCacheOverridesStaleState(t *testing.T) {
	cache := cachePath(t)
	c := startCache(t, cache, "auto", "Rule")
	if err := c.Select("proxy", "hk1"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetMode("Direct"); err != nil {
		t.Fatal(err)
	}
	_ = c.Stop()

	// 前提:不改缓存时内核确实按缓存跑(sing-box 的行为);前提不成立这条测试就没意义了
	c = startCache(t, cache, "auto", "Global")
	if now, mode := proxyNow(t, c), c.Mode(); now != "hk1" || !strings.EqualFold(mode, "Direct") {
		t.Fatalf("前提不成立:没改缓存时应按缓存起在 hk1 / Direct,实际 %s / %s", now, mode)
	}
	_ = c.Stop()

	if err := PresetCache(cache, "Global", "proxy", "auto"); err != nil {
		t.Fatal(err)
	}
	c = startCache(t, cache, "auto", "Global")
	defer c.Stop()
	if now := proxyNow(t, c); now != "auto" {
		t.Fatalf("改过缓存,proxy 组应落在配置的 auto,实际 %s", now)
	}
	if mode := c.Mode(); !strings.EqualFold(mode, "Global") {
		t.Fatalf("改过缓存,模式应是配置的 Global,实际 %s —— 严格全局下按别的模式跑就是经 direct 从本服务直连", mode)
	}
}
