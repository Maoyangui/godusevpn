package uiapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
)

// Android 上下好的 APK 交给系统安装器装完就一直躺在数据目录里(电视盒子空间本来就紧);
// Linux 上解包失败那次的安装包也会留到下次升级。服务启动时把当前版本及更旧的安装包清掉,比当前新的留着。
func TestRunCleansInstalledUpdates(t *testing.T) {
	data := t.TempDir()
	t.Setenv("GODUSEVPN_DATA", data)
	oldVer := buildinfo.Version
	buildinfo.Version = "0.7.17"
	t.Cleanup(func() { buildinfo.Version = oldVer })
	dir := updateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(dir, "godusevpn-0.7.17-android-arm64.apk")
	older := filepath.Join(dir, "godusevpn-0.7.16-android-arm64.apk")
	for _, p := range []string{installed, older} {
		if err := os.WriteFile(p, []byte("apk"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := New(&secretBackend{}, Options{Platform: "android"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Run(ctx)
	for _, p := range []string{installed, older} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("服务启动后旧安装包还在: %s (%v)", p, err)
		}
	}
}
