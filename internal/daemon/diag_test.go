package daemon

import (
	"archive/zip"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

// diagDaemon 一个带着真实形态订阅的守护进程:m-ui 默认拿用户名当订阅路径(/sub/alice),订阅名也是它;
// 当前订阅里有一个 socks 节点(用户名同样是 alice)。服务日志里有一行老样子的拉取失败(带完整地址)。
func diagDaemon(t *testing.T) *Daemon {
	t.Helper()
	t.Setenv("GODUSEVPN_DATA", t.TempDir())
	t.Setenv("GODUSEVPN_CONF", t.TempDir())
	d, err := NewWithOptions(Options{NoListen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	s := settings.Default()
	s.Profiles = []settings.Profile{{ID: "a", Name: "alice", URL: "https://panel.example:2056/sub/alice"}}
	s.ActiveProfile = "a"
	s.WebPassword = "c2FsdA$0123456789abcdef"
	if err := d.setSettings(s); err != nil {
		t.Fatal(err)
	}
	d.setProfileCache("a", &profile.Profile{URL: s.Profiles[0].URL, Title: "alice", FetchedAt: time.Now().Unix(), Tags: []string{"HK"},
		Outbounds: []json.RawMessage{json.RawMessage(`{"type":"socks","tag":"HK","server":"1.2.3.4","server_port":1080,"username":"alice","password":"S5PW"}`)}})
	d.logf("订阅经 proxy 拉取失败: %s", `拉取订阅失败: Get "https://panel.example:2056/sub/alice?format=json": timeout`)
	d.coreLog.Printf("INFO", "dns: lookup succeed for www.visited-site.com: 142.250.72.4")
	return d
}

// 诊断输出里不能出现的:订阅令牌(地址里、订阅名里、节点用户名里)、节点密码、面板密码哈希、
// 内核日志里访问过的域名与目标公网地址(合起来就是浏览记录)。
func assertDiagClean(t *testing.T, where, s string) {
	t.Helper()
	for _, secret := range []string{"alice", "S5PW", "0123456789abcdef", "/sub/", "visited-site", "142.250.72.4"} {
		if strings.Contains(s, secret) {
			t.Errorf("%s 里还有 %q:\n%s", where, secret, s)
		}
	}
}

// 命令行 diag(文档说"要发给别人看就导这个,订阅地址已打码")以前原样返回状态与日志。
// 现在和诊断包同一套打码;排障要的主机、节点地址、失败原因都还在,运行中的设置不受影响。
func TestDiagnoseRedacts(t *testing.T) {
	d := diagDaemon(t)
	out, err := d.diagnose()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	assertDiagClean(t, "diag 输出", string(b))
	for _, keep := range []string{`panel.example:2056/***`, `"1.2.3.4:1080"`, "timeout", `"serviceLog"`, `"coreLog"`, `"webPassword":"***"`} {
		if !strings.Contains(string(b), keep) {
			t.Errorf("diag 输出缺了 %s:\n%s", keep, b)
		}
	}
	if got := d.getSettings(); got.Profiles[0].URL != "https://panel.example:2056/sub/alice" || got.WebPassword != "c2FsdA$0123456789abcdef" {
		t.Fatalf("打码改到了运行中的设置: %+v", got.Profiles[0])
	}
}

// 诊断包里每个文件都过同一套打码;info.json / config.redacted.json 仍是合法 JSON。
// 系统网络信息里本机的主机名也打了码(Windows 的 ipconfig /all 里有它)。
func TestExportDiagRedacts(t *testing.T) {
	d := diagDaemon(t)
	cfg := `{"outbounds":[{"type":"socks","tag":"HK","server":"1.2.3.4","server_port":1080,"username":"alice","password":"S5PW"},
	 {"type":"hysteria","tag":"hy1","server":"1.2.3.4","obfs":"OBFSSECRET"},{"type":"ssh","tag":"ssh","user":"root","private_key_passphrase":"PASSPHRASE1"}]}`
	if err := os.WriteFile(paths.Config(), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := d.exportDiag()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	host, _ := os.Hostname()
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
		assertDiagClean(t, f.Name, string(b))
		for _, secret := range []string{"OBFSSECRET", "PASSPHRASE1", `"root"`} {
			if strings.Contains(string(b), secret) {
				t.Errorf("%s 里还有 %q", f.Name, secret)
			}
		}
		if (f.Name == "ipconfig.txt" || f.Name == "ifconfig.txt") && len(host) >= 2 && strings.Contains(strings.ToLower(string(b)), strings.ToLower(host)) {
			t.Errorf("%s 里还有本机主机名", f.Name)
		}
	}
	for _, name := range []string{"info.json", "config.redacted.json"} {
		var v any
		if err := json.Unmarshal([]byte(files[name]), &v); err != nil {
			t.Fatalf("%s 不是合法 JSON: %v", name, err)
		}
	}
	if !strings.Contains(files["service.log"], "panel.example:2056/***") || !strings.Contains(files["info.json"], `"1.2.3.4:1080"`) {
		t.Fatalf("排障要的主机 / 节点地址丢了:\n%s\n%s", files["service.log"], files["info.json"])
	}
}

// 导出诊断包会把订阅链接脱敏成 /sub/***;它拿到的必须是副本,真实设置不能跟着变(曾经因共享切片把链接改坏,之后刷新全是 404)。
func TestExportDiagKeepsSettingsIntact(t *testing.T) {
	t.Setenv("GODUSEVPN_DATA", t.TempDir())
	t.Setenv("GODUSEVPN_CONF", t.TempDir())
	d, err := NewWithOptions(Options{NoListen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	s := settings.Default()
	s.Profiles = []settings.Profile{{ID: "a", Name: "a", URL: "https://panel.example/sub/secret-token"}}
	s.ActiveProfile = "a"
	if err := d.setSettings(s); err != nil {
		t.Fatal(err)
	}
	if _, err := d.exportDiag(); err != nil {
		t.Fatal(err)
	}
	if got := d.getSettings().Profiles[0].URL; got != "https://panel.example/sub/secret-token" {
		t.Fatalf("导出诊断包后设置里的订阅链接被改成了 %q", got)
	}
	if _, err := d.refreshProfile(t.Context(), "a"); err == nil {
		t.Fatal("拉不到的链接应报错")
	}
}

// 已经被写坏的设置(0.6.0-a2 之前):启动时按订阅缓存把链接恢复回来。
func TestHealsRedactedProfileURLOnStart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GODUSEVPN_DATA", dir)
	t.Setenv("GODUSEVPN_CONF", dir)
	real := "https://panel.example/sub/secret-token"
	s := settings.Default()
	s.Profiles = []settings.Profile{{ID: "a", Name: "a", URL: "https://panel.example/sub/***"}}
	s.ActiveProfile = "a"
	if err := paths.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(paths.Settings()); err != nil {
		t.Fatal(err)
	}
	p := &profile.Profile{URL: real, FetchedAt: time.Now().Unix(), Tags: []string{"n"}, Outbounds: []json.RawMessage{json.RawMessage(`{"type":"direct","tag":"n"}`)}}
	if err := p.Save(paths.ProfileCache("a")); err != nil {
		t.Fatal(err)
	}
	d, err := NewWithOptions(Options{NoListen: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if got := d.getSettings().Profiles[0].URL; got != real {
		t.Fatalf("启动时应按缓存恢复链接,得到 %q", got)
	}
	reloaded, err := settings.Load(paths.Settings())
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Profiles[0].URL != real {
		t.Fatalf("恢复后应写回磁盘,得到 %q", reloaded.Profiles[0].URL)
	}
}
