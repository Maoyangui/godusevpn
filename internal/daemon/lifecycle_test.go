package daemon

import (
	"strings"
	"testing"
)

// before 断言 fn 里 a 出现在 b 之前(两者都得在)。
func before(t *testing.T, fn, a, b, why string) {
	t.Helper()
	i, j := strings.Index(fn, a), strings.Index(fn, b)
	if i < 0 || j < 0 || i > j {
		t.Fatalf("%s 应出现在 %s 之前:%s", a, b, why)
	}
}

// 内核启动时先读缓存里记着的模式与选中节点、盖过配置(core.PresetCache 那条测试钉着 sing-box 的这个行为)。
// 守护进程必须在起内核之前把缓存对齐到这一轮配置,不能只在起来之后再 Select —— 那时 TUN 已经在收流量了。
func TestStartPresetsCacheBeforeCore(t *testing.T) {
	fn := funcBody(t, readDaemonSource(t), "func (d *Daemon) start(cfg []byte) error {")
	before(t, fn, "core.PresetCache(", "d.core.Start(cfg)", "缓存里的旧模式 / 旧节点会在内核启动时盖过配置")
}
