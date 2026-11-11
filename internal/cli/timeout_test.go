package cli

import (
	"testing"

	"github.com/Maoyangui/godusevpn/internal/ipc"
)

// 命令行等一条命令不短于守护进程换新配置的上限(见 ipc.SlowCallTimeout)。
func TestCallTimeout(t *testing.T) {
	if callTimeout < ipc.SlowCallTimeout {
		t.Fatalf("callTimeout=%v 比 ipc.SlowCallTimeout=%v 短", callTimeout, ipc.SlowCallTimeout)
	}
}
