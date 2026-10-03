package netmode

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// 停用与还原网卡 IPv6 跨进程互斥(守护进程与「恢复网络」各是一个进程):第二个持有者必须等第一个放锁,
// 等不到就超时报错,而不是两边同时改备份和绑定。用一个测试专用的名字,不碰产品的那把锁;
// 建全局对象要管理员,没权限就跳过。
func TestNamedLockSerializes(t *testing.T) {
	name := fmt.Sprintf(`Global\godusevpn-test-lock-%d`, os.Getpid())
	inside, release, first := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		first <- withNamedLock(name, time.Minute, func() error { close(inside); <-release; return nil })
	}()
	select {
	case <-inside:
	case err := <-first:
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skipf("建不了全局互斥体(要管理员): %v", err)
		}
		t.Fatalf("第一个持有者没拿到锁: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("第一个持有者迟迟拿不到锁")
	}

	if err := withNamedLock(name, 200*time.Millisecond, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "还没完") {
		t.Fatalf("锁被占着时第二个持有者应当等到超时报错,得到 %v", err)
	}
	second := make(chan error, 1)
	go func() { second <- withNamedLock(name, time.Minute, func() error { return nil }) }()
	select {
	case err := <-second:
		t.Fatalf("第二个持有者没等第一个放锁就跑了: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	for _, c := range []chan error{first, second} {
		select {
		case err := <-c:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("放锁之后没轮到下一个")
		}
	}
}
