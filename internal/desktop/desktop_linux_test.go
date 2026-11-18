package desktop

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sandbox(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	oa, oi, oh := appsDir, iconsDir, hintPath
	appsDir, iconsDir, hintPath = filepath.Join(dir, "applications"), filepath.Join(dir, "icons", "hicolor", "scalable", "apps"), filepath.Join(dir, "run", "godusevpn.panel")
	os.MkdirAll(filepath.Dir(hintPath), 0o755)
	t.Cleanup(func() { appsDir, iconsDir, hintPath = oa, oi, oh })
	return dir
}

// 入口文件:登记了 godusevpn 协议、把链接作为 %u 交给 open-link;有桌面才装,内容没变不重写,卸载删干净。
func TestInstallEntry(t *testing.T) {
	sandbox(t)
	if Available() {
		t.Fatal("应用目录还不存在")
	}
	if changed, err := Install("/usr/local/bin/godusevpn", []byte("<svg/>")); changed || err != nil {
		t.Fatalf("没有桌面的机器什么都不做: %v %v", changed, err)
	}
	if _, err := os.Stat(iconsDir); err == nil {
		t.Fatal("没有桌面时不该建图标目录")
	}
	os.MkdirAll(appsDir, 0o755)
	changed, err := Install("/usr/local/bin/godusevpn", []byte("<svg/>"))
	if !changed || err != nil {
		t.Fatalf("第一次应当写入: %v %v", changed, err)
	}
	b, _ := os.ReadFile(filepath.Join(appsDir, entryName))
	entry := string(b)
	for _, want := range []string{"[Desktop Entry]\n", "\nExec=/usr/local/bin/godusevpn open-link %u\n", "\nMimeType=x-scheme-handler/godusevpn;\n", "\nIcon=godusevpn\n", "\nName[zh_CN]=佛跳墙\n", "\nTerminal=false\n"} {
		if !strings.Contains(entry, want) {
			t.Errorf("入口文件里应当有 %q:\n%s", want, entry)
		}
	}
	if icon, _ := os.ReadFile(filepath.Join(iconsDir, iconName)); string(icon) != "<svg/>" {
		t.Fatalf("图标没写对: %q", icon)
	}
	if changed, _ := Install("/usr/local/bin/godusevpn", []byte("<svg/>")); changed {
		t.Fatal("内容没变不该重写(守护进程每次启动都会来对一遍)")
	}
	if changed, _ := Install("/opt/bin/godusevpn", []byte("<svg/>")); !changed {
		t.Fatal("程序换了位置要跟着改")
	}
	WriteHint("0.0.0.0:9800")
	Uninstall()
	for _, p := range []string{filepath.Join(appsDir, entryName), filepath.Join(iconsDir, iconName), hintPath} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("卸载后还留着 %s", p)
		}
	}
}

func TestQuoteExec(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/local/bin/godusevpn": "/usr/local/bin/godusevpn",
		"/opt/my apps/godusevpn":   `"/opt/my apps/godusevpn"`,
		`/opt/a"b$c/godusevpn`:     `"/opt/a\"b\$c/godusevpn"`,
	} {
		if got := quoteExec(in); got != want {
			t.Errorf("quoteExec(%q) = %q,要 %q", in, got, want)
		}
	}
}

// 提示文件:所有人可读、内容是本机能打开的地址;面板关了就删;内容不像地址的不认。
func TestPanelHint(t *testing.T) {
	sandbox(t)
	if PanelURL() != "" {
		t.Fatal("没有提示文件时是空的")
	}
	WriteHint("0.0.0.0:9800")
	if got := PanelURL(); got != "http://127.0.0.1:9800/" {
		t.Fatalf("面板地址: %q", got)
	}
	if st, err := os.Stat(hintPath); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("提示文件要所有人可读: %v %v", st, err)
	}
	WriteHint("192.168.1.1:9801")
	if got := PanelURL(); got != "http://192.168.1.1:9801/" {
		t.Fatalf("改了监听地址要跟着变: %q", got)
	}
	WriteHint("")
	if PanelURL() != "" {
		t.Fatal("面板关了要删掉提示文件")
	}
	os.WriteFile(hintPath, []byte("javascript:alert(1)\n"), 0o644)
	if PanelURL() != "" {
		t.Fatal("内容不是 http 地址的不认")
	}
}

// 提示文件可能是进程被强杀后留下的:连不上的地址不算面板在。
func TestAlive(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := "http://" + ln.Addr().String() + "/"
	if !Alive(u) {
		t.Fatal("在监听的地址应当算活着")
	}
	ln.Close()
	if Alive(u) || Alive("") || Alive("不是地址") {
		t.Fatal("没人监听 / 写法不对的都不算")
	}
}

// 打开浏览器:BROWSER 指定了一个能找到的程序就用它,否则按顺序找 xdg-open 等。
func TestOpenPicksBrowser(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"mybrowser", "xdg-open"} {
		os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o755)
	}
	t.Setenv("PATH", dir)
	var got []string
	old := run
	run = func(name string, args ...string) error {
		got = append([]string{filepath.Base(name)}, args...)
		return nil
	}
	t.Cleanup(func() { run = old })
	t.Setenv("BROWSER", "")
	if err := Open("http://127.0.0.1:9800/#import?url=x"); err != nil || strings.Join(got, " ") != "xdg-open http://127.0.0.1:9800/#import?url=x" {
		t.Fatalf("应当用 xdg-open: %v %v", got, err)
	}
	t.Setenv("BROWSER", "mybrowser")
	if err := Open("http://127.0.0.1:9800/"); err != nil || got[0] != "mybrowser" {
		t.Fatalf("BROWSER 指定了就用它: %v %v", got, err)
	}
	t.Setenv("BROWSER", "firefox %s:chromium") // 带参数、多个候选的写法不解析,退回 xdg-open
	if err := Open("http://127.0.0.1:9800/"); err != nil || got[0] != "xdg-open" {
		t.Fatalf("复杂的 BROWSER 写法退回 xdg-open: %v %v", got, err)
	}
	os.Remove(filepath.Join(dir, "xdg-open"))
	t.Setenv("BROWSER", "")
	if err := Open("http://127.0.0.1:9800/"); err == nil || !strings.Contains(err.Error(), "http://127.0.0.1:9800/") {
		t.Fatalf("什么都没有时要说清楚并给出地址: %v", err)
	}
}
