package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaultValidAndRoundTrip(t *testing.T) {
	s := Default()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "settings.json")
	s.Profiles = []Profile{{ID: "a1", Name: "主站", URL: "https://example.com/sub/alice"}}
	s.ActiveProfile = "a1"
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, s) {
		t.Fatalf("读回不一致:\n%+v\n%+v", got, s)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err != nil {
		t.Fatal("文件不存在应给默认值")
	}
}

// schema 1 的单个 profileUrl 要迁成订阅列表
func TestMigrateFromSchema1(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(`{"schema":1,"profileUrl":"https://x.example/sub/u","mode":"rule","tun":true,"tunStack":"mixed","mixedPort":2080,"remoteDns":"1.1.1.1","localDns":"223.5.5.5","updateHours":6,"logLevel":"info","clashPort":9090}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Profiles) != 1 || s.Profiles[0].URL != "https://x.example/sub/u" || s.ActiveProfile != s.Profiles[0].ID || s.ProfileURL != "" {
		t.Fatalf("迁移不对: %+v", s)
	}
	if s.Schema != Schema || s.ProbeMinutes != 3 {
		t.Fatalf("新字段应补默认值: %+v", s)
	}
	if s.Active() == nil || s.Active().Name != "默认" {
		t.Fatal("迁移出的订阅应叫默认并处于选中")
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	cases := []func(*Settings){
		func(s *Settings) { s.Mode = "auto" },
		func(s *Settings) { s.TUNStack = "netstack" },
		func(s *Settings) { s.TUN, s.MixedPort = false, 0 },
		func(s *Settings) { s.MixedPort = s.ClashPort },
		func(s *Settings) { s.RemoteDNS = "not a host" },
		func(s *Settings) { s.UpdateHours = 0 },
		func(s *Settings) { s.ProbeMinutes = 0 },
		func(s *Settings) { s.Profiles = []Profile{{ID: "x", URL: "ftp://x"}} },
		func(s *Settings) { s.Profiles = []Profile{{ID: "x", URL: "https://a/"}, {ID: "x", URL: "https://b/"}} },
	}
	for i, mut := range cases {
		s := Default()
		mut(&s)
		if err := s.Validate(); err == nil {
			t.Fatalf("用例 %d 应被拒绝", i)
		}
	}
}

func TestValidateFixesActiveAndNames(t *testing.T) {
	s := Default()
	s.Profiles = []Profile{{ID: "a", URL: "https://a/"}, {ID: "b", Name: " 备用 ", URL: " https://b/ "}}
	s.ActiveProfile = "nope"
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.ActiveProfile != "a" || s.Profiles[0].Name != "订阅 1" || s.Profiles[1].Name != "备用" || s.Profiles[1].URL != "https://b/" {
		t.Fatalf("应回退到第一条、补名字、去空白: %+v", s)
	}
	s.Profiles = nil
	_ = s.Validate()
	if s.ActiveProfile != "" || s.Active() != nil {
		t.Fatal("没有订阅时 active 应为空")
	}
}

func TestApplyKeyValue(t *testing.T) {
	s := Default()
	s2, err := s.Apply("mode", "global")
	if err != nil || s2.Mode != ModeGlobal {
		t.Fatalf("改模式失败: %v %+v", err, s2)
	}
	s3, err := s2.Apply("ipv6", "on")
	if err != nil || !s3.IPv6 {
		t.Fatalf("布尔值应能用 on/off: %v", err)
	}
	s4, err := s3.Apply("mixedPort", "7890")
	if err != nil || s4.MixedPort != 7890 {
		t.Fatalf("数字应能改: %v", err)
	}
	if _, err := s4.Apply("noSuchKey", "1"); err == nil {
		t.Fatal("未知键应报错")
	}
	if _, err := s4.Apply("mode", "x"); err == nil {
		t.Fatal("改成非法值应报错")
	}
}

func TestBypassAppsNormalizedAndValidated(t *testing.T) {
	s := Default()
	s.BypassApps = []string{" steam.exe ", "", "Game.exe"}
	if err := s.Validate(); err != nil || len(s.BypassApps) != 2 || s.BypassApps[0] != "steam.exe" {
		t.Fatalf("进程名应去空白、去空项: %v %v", err, s.BypassApps)
	}
	s.BypassApps = []string{"C:/x/steam.exe"}
	if err := s.Validate(); err == nil {
		t.Fatal("带路径的进程名应被拒绝")
	}
}

func TestCloneIsDeep(t *testing.T) {
	s := Default()
	s.Profiles = []Profile{{ID: "a", Name: "a", URL: "https://x/sub/token"}}
	s.BypassApps = []string{"steam.exe"}
	s.RuleGroups = []RuleGroup{{ID: "g", Name: "g", Enabled: true, Outbound: OutDirect, Rules: []Rule{{Type: RuleDomain, Value: "a.com"}}}}
	c := s.Clone()
	c.Profiles[0].URL = "https://x/sub/***"
	c.BypassApps[0] = "x"
	c.RuleGroups[0].Rules[0].Value = "b.com"
	if s.Profiles[0].URL != "https://x/sub/token" || s.BypassApps[0] != "steam.exe" || s.RuleGroups[0].Rules[0].Value != "a.com" {
		t.Fatalf("Clone 改副本不能影响原设置: %+v", s)
	}
}
