package netmode

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain 整个包的测试都把数据目录 / 配置目录指到临时目录。
// 这个包的函数会读写数据目录里的网卡备份与"网卡原值丢失"记录;曾有测试没隔离,往开发机真实的数据目录写进一条
// 假记录 —— 装了产品的机器上首页会因此挂出假提示。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "godusevpn-netmode-test-*")
	if err != nil {
		panic(err)
	}
	for _, sub := range []string{"data", "conf"} {
		_ = os.MkdirAll(filepath.Join(dir, sub), 0o755)
	}
	os.Setenv("GODUSEVPN_DATA", filepath.Join(dir, "data"))
	os.Setenv("GODUSEVPN_CONF", filepath.Join(dir, "conf"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
