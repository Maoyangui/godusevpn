package settings

import (
	"path/filepath"
	"testing"
)

func TestDefaultValidAndRoundTrip(t *testing.T) {
	s := Default()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "settings.json")
	s.ProfileURL = "https://example.com/sub/alice?format=json"
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != s {
		t.Fatalf("读回不一致:\n%+v\n%+v", got, s)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err != nil {
		t.Fatal("文件不存在应给默认值")
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
		func(s *Settings) { s.ProfileURL = "ftp://x" },
	}
	for i, mut := range cases {
		s := Default()
		mut(&s)
		if err := s.Validate(); err == nil {
			t.Fatalf("用例 %d 应被拒绝", i)
		}
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
