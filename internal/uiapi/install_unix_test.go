//go:build !windows

package uiapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// macOS 的发布包里有两个文件都叫 godusevpn:根目录下的守护进程,和
// godusevpn.app/Contents/MacOS/godusevpn 那个图形界面。早先是按 basename 认、第一个命中就用,
// tar 里谁在前面就抽谁 —— 抽到界面那一份,/usr/local/bin/godusevpn 会被换成一个 Cocoa 窗口程序,
// launchd 以 root 反复拉起它,VPN 彻底不能用,连 `godusevpn uninstall` 都没了,只能重装。
// 这里按真实的包结构造一份,而且**故意把界面那一份放在前面**。
func TestExtractBinaryPicksTheDaemonNotTheApp(t *testing.T) {
	cases := []struct {
		name    string
		entries []tarEntry
		want    string
	}{
		{
			"macOS:界面排在守护进程前面",
			[]tarEntry{
				{"./godusevpn.app/Contents/MacOS/godusevpn", "我是图形界面"},
				{"./godusevpn.app/Contents/Info.plist", "<plist/>"},
				{"./godusevpn", "我是守护进程"},
			},
			"我是守护进程",
		},
		{
			"名字不带 ./ 前缀的写法",
			[]tarEntry{
				{"godusevpn.app/Contents/MacOS/godusevpn", "我是图形界面"},
				{"godusevpn", "我是守护进程"},
			},
			"我是守护进程",
		},
		{
			"Linux:包里只有一个",
			[]tarEntry{{"./godusevpn", "我是守护进程"}},
			"我是守护进程",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			archive := writeTarGz(t, c.entries)
			dst := filepath.Join(t.TempDir(), "out")
			if err := extractBinary(archive, dst); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(dst)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != c.want {
				t.Fatalf("抽错了文件:拿到 %q,应该是 %q", got, c.want)
			}
		})
	}
}

// 包里只有 .app 里那一份时要明确报错,而不是把界面当守护进程装上去。
func TestExtractBinaryRefusesAppOnly(t *testing.T) {
	archive := writeTarGz(t, []tarEntry{{"./godusevpn.app/Contents/MacOS/godusevpn", "我是图形界面"}})
	dst := filepath.Join(t.TempDir(), "out")
	if err := extractBinary(archive, dst); err == nil {
		t.Fatal("包里没有守护进程,却装了")
	}
}

type tarEntry struct{ name, body string }

func writeTarGz(t *testing.T, entries []tarEntry) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "pkg.tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
