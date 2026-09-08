package daemon

import (
	"encoding/json"
	"time"

	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

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
