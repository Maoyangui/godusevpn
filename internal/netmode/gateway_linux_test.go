//go:build linux && !android

package netmode

import (
	"os"
	"path/filepath"
	"testing"
)

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 本来关着:连着时打开,不想连了关回去,记录清掉。
func TestForwardRestoredWhenWeEnabledIt(t *testing.T) {
	dir := t.TempDir()
	sysctl, mark := filepath.Join(dir, "ip_forward"), filepath.Join(dir, "mark")
	if err := os.WriteFile(sysctl, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := enableForward(sysctl, mark); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, sysctl); got != "1\n" {
		t.Fatalf("应打开转发: %q", got)
	}
	// 重连时再来一遍:已经是开的,记录要留着,否则断开时就关不回去了
	if err := enableForward(sysctl, mark); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mark); err != nil {
		t.Fatal("记录不该丢")
	}
	restoreForward(sysctl, mark)
	if got := readFile(t, sysctl); got != "0\n" {
		t.Fatalf("应关回去: %q", got)
	}
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Fatal("记录应清掉")
	}
}

// 本来就开着(OpenWrt):不记录,断开时也不动。
func TestForwardLeftAloneWhenAlreadyOn(t *testing.T) {
	dir := t.TempDir()
	sysctl, mark := filepath.Join(dir, "ip_forward"), filepath.Join(dir, "mark")
	if err := os.WriteFile(sysctl, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := enableForward(sysctl, mark); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Fatal("不是我们开的不该有记录")
	}
	restoreForward(sysctl, mark)
	if got := readFile(t, sysctl); got != "1\n" {
		t.Fatalf("不是我们开的不该关: %q", got)
	}
}

// 记录是上次开机留下的:转发现在的状态和它无关,不能关。
func TestForwardStaleMarkIgnored(t *testing.T) {
	dir := t.TempDir()
	sysctl, mark := filepath.Join(dir, "ip_forward"), filepath.Join(dir, "mark")
	if err := os.WriteFile(sysctl, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mark, []byte("another-boot"), 0o600); err != nil {
		t.Fatal(err)
	}
	restoreForward(sysctl, mark)
	if got := readFile(t, sysctl); got != "1\n" {
		t.Fatalf("上次开机的记录不该生效: %q", got)
	}
	if _, err := os.Stat(mark); !os.IsNotExist(err) {
		t.Fatal("过期记录应清掉")
	}
}
