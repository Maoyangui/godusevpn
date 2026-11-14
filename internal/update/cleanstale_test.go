package update

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 升级成功、新版启动后:装过的(当前版本)和更旧的安装包、解压目录都清掉;比当前版本新的留着
// (可能是刚下好、还等着系统安装器装的那一个,比如 Android 上用户还没在安装界面点确认)。
func TestCleanStaleKeepsOnlyNewerInstallers(t *testing.T) {
	dir := t.TempDir()
	for _, v := range []string{"0.6.3", "0.7.9", "0.7.16", "0.7.17-rc.1", "0.7.17"} {
		touch(t, filepath.Join(dir, assetName(v)))
	}
	touch(t, filepath.Join(dir, assetName("0.7.18")))
	touch(t, filepath.Join(dir, assetName("0.8.0-m1")))
	touch(t, filepath.Join(dir, "something-else.tmp"))
	if err := os.MkdirAll(filepath.Join(dir, "unpacked", "godusevpn.app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CleanStale(dir, "0.7.17"); err != nil {
		t.Fatal(err)
	}
	want := []string{assetName("0.7.18"), assetName("0.8.0-m1")}
	sort.Strings(want)
	if got := listDir(t, dir); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("清理后剩下 %v,应该只剩 %v", got, want)
	}
	// 目录不存在不算错(从没升级过)
	if err := CleanStale(filepath.Join(dir, "nope"), "0.7.17"); err != nil {
		t.Fatalf("目录不存在: %v", err)
	}
	// 当前版本号认不出来(本地构建)时什么都不删,免得把等着装的新版删掉
	if err := CleanStale(dir, "dev"); err != nil {
		t.Fatal(err)
	}
	if got := listDir(t, dir); len(got) != 2 {
		t.Fatalf("版本号认不出来时删了东西: %v", got)
	}
}

func TestInstallerVersion(t *testing.T) {
	for _, v := range []string{"0.7.17", "0.7.17-rc.1", "1.0.0-m3"} {
		if got, ok := installerVersion(assetName(v)); !ok || got != v {
			t.Fatalf("installerVersion(%q) = %q, %v", assetName(v), got, ok)
		}
	}
	for _, n := range []string{"unpacked", "godusevpn-0.7.17.part", "godusevpn-x.y.z" + strings.TrimPrefix(assetName("0"), "godusevpn-0"), "other-0.7.17" + strings.TrimPrefix(assetName("0"), "godusevpn-0")} {
		if v, ok := installerVersion(n); ok {
			t.Fatalf("installerVersion(%q) 认成了 %q", n, v)
		}
	}
}
