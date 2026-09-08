//go:build !windows

package ipc

import (
	"os"
	"path/filepath"
)

// 测试不以 root 跑,socket 放到临时目录
func init() {
	SocketPath = filepath.Join(os.TempDir(), "godusevpn-test-"+randomSuffix()+".sock")
}

func randomSuffix() string {
	b := make([]byte, 4)
	f, err := os.Open("/dev/urandom")
	if err == nil {
		_, _ = f.Read(b)
		f.Close()
	}
	const hex = "0123456789abcdef"
	out := make([]byte, 8)
	for i, c := range b {
		out[2*i], out[2*i+1] = hex[c>>4], hex[c&15]
	}
	return string(out)
}
