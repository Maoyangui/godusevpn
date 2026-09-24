package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain 整个包的测试都把数据目录 / 配置目录指到临时目录。
// 守护进程的代码处处读写数据目录(状态、设置、网卡备份);测试绝不能碰装了产品的机器上的真实文件。
func TestMain(m *testing.M) {
	// crash_test 的子进程:数据目录由父进程给(它会崩,走不到下面的清理,不能再建一个临时目录留在那里)
	if dir := os.Getenv("GODUSEVPN_CRASH_CHILD"); dir != "" {
		os.Setenv("GODUSEVPN_DATA", dir)
		os.Setenv("GODUSEVPN_CONF", filepath.Join(dir, "conf"))
		os.Exit(m.Run())
	}
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
