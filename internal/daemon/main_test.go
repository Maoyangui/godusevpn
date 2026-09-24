package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain 整个包的测试都把数据目录 / 配置目录指到临时目录。
// 守护进程的代码处处读写数据目录(状态、设置、网卡备份);测试绝不能碰装了产品的机器上的真实文件。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "godusevpn-daemon-test-*")
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
