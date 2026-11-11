package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	if s.Schema != Schema || s.ProbeMinutes != DefaultProbeMinutes {
		t.Fatalf("新字段应补默认值: %+v", s)
	}
	if !s.DisableNICIPv6 {
		t.Fatal("网卡级停用 IPv6 是后加的项,老设置文件迁移过来要按新默认值补成开启")
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

// 读不出来的设置文件在被覆盖前另存一份;不存在就什么都不做。
func TestPreserve(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if bak, err := Preserve(p); err != nil || bak != "" {
		t.Fatalf("文件不存在时不该另存:%q %v", bak, err)
	}
	if err := os.WriteFile(p, []byte("{坏了"), 0o600); err != nil {
		t.Fatal(err)
	}
	bak, err := Preserve(p)
	if err != nil || !strings.HasPrefix(bak, p+".bad-") {
		t.Fatalf("应另存为 .bad-<时间>:%q %v", bak, err)
	}
	if b, _ := os.ReadFile(bak); string(b) != "{坏了" {
		t.Fatal("另存的内容不对")
	}
	if b, _ := os.ReadFile(p); string(b) != "{坏了" {
		t.Fatal("原文件不该被动")
	}
}

// 原来的默认值 3 分钟跟着新默认走,只迁一次:迁过之后再自己设成 3 就是 3。
func TestMigrateProbeMinutes(t *testing.T) {
	load := func(body string) Settings {
		t.Helper()
		p := filepath.Join(t.TempDir(), "settings.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, c := range []struct {
		body string
		want int
	}{
		{`{"schema":4,"probeMinutes":3,"mode":"rule"}`, DefaultProbeMinutes},
		{`{"schema":4,"probeMinutes":7,"mode":"rule"}`, 7},
		{`{"schema":4,"probeMinutes":3,"probeMoved":true,"mode":"rule"}`, 3},
		{`{"schema":4,"mode":"rule"}`, DefaultProbeMinutes},
	} {
		s := load(c.body)
		if s.ProbeMinutes != c.want || !s.ProbeMoved {
			t.Fatalf("%s:得 %d,迁过标记 %v", c.body, s.ProbeMinutes, s.ProbeMoved)
		}
	}
}

// 旧版读到比自己新的 schema 会拒绝启动(Load),用户退回旧版服务就起不来。加字段不用升 schema:
// 旧版会忽略不认识的键;一次性的迁移用单独的标记,像 ProbeMoved 那样。真要升,先想清楚退回旧版怎么办。
func TestSchemaStaysReadableByOldClients(t *testing.T) {
	if Schema != 4 {
		t.Fatalf("schema 变成了 %d,v0.7.1 到 v0.7.15 都读不了这份设置", Schema)
	}
}

// 「线路自动」是后加的键,不升 schema:新版读得到;旧版读设置时忽略它(退回旧版照样起得来,开关回到关)。
// 命令行按 key=value 也能改它(服务那边由 SetLineAuto 接手)。
func TestLineAutoKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(`{"schema":4,"lineAuto":true,"mode":"rule","selected":"香港1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !s.LineAuto || s.Selected != "香港1" || s.Schema != 4 {
		t.Fatalf("读不到 lineAuto: %+v", s.LineAuto)
	}
	if s.LineAuto = false; Default().LineAuto {
		t.Fatal("默认关")
	}
	if next, err := s.Apply("lineAuto", "on"); err != nil || !next.LineAuto {
		t.Fatalf("命令行改 lineAuto: %v", err)
	}
}

// 选了线路组之后退回旧版本:旧版不认识线路组,把组名当成手动选的节点(找不到就回落自动选择,还会把定时测速、离开直连时的现测
// 一起停掉)。所以选线路组时磁盘上 selected 写组里的一台(旧版认得的节点),组名另存在旧版会忽略的 selectedGroup 里;
// 新版读回来还是线路组。不升 schema;控制口(界面、命令行)看到的照旧是 selected = 线路组。
func TestSelectedGroupOnDiskForOldClients(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	s := Default()
	s.Selected, s.SelectedNode, s.LineAuto = "香港1", "香港1-香港", true
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// 旧版(v0.7.17 及更早)怎么读:同一个 schema,不认识的键忽略,selected 当节点用
	var old struct {
		Schema   int    `json:"schema"`
		Selected string `json:"selected"`
	}
	if err := json.Unmarshal(raw, &old); err != nil || old.Schema != 4 || old.Selected != "香港1-香港" {
		t.Fatalf("旧版读到的 selected 应是组里的一台节点: %+v %v", old, err)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if m["selectedGroup"] != "香港1" {
		t.Fatalf("线路组另存在 selectedGroup: %v", m["selectedGroup"])
	}
	got, err := Load(p)
	if err != nil || got.Selected != "香港1" || got.SelectedNode != "香港1-香港" || got.SelectedGroup != "" || !got.LineAuto {
		t.Fatalf("新版读回来还是线路组: %+v %v", got.Selected, err)
	}
	// 控制口上的样子不变:selected 是线路组,不带 selectedGroup / 那一台
	b, _ := json.Marshal(got)
	if !strings.Contains(string(b), `"selected":"香港1"`) || strings.Contains(string(b), "selectedGroup") || strings.Contains(string(b), "香港1-香港") {
		t.Fatalf("控制口上照旧: %s", b)
	}
	// 选的是节点 / 自动选择:原样写,没有 selectedGroup
	for _, sel := range []string{"美国", ""} {
		s := Default()
		s.Selected = sel
		if err := s.Save(p); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(p)
		m = nil
		_ = json.Unmarshal(raw, &m)
		if m["selected"] != sel || m["selectedGroup"] != nil {
			t.Fatalf("选 %q:%s", sel, raw)
		}
	}
	// 前一版存的(selected 直接是组名,没有 selectedGroup):照样读成线路组
	if err := os.WriteFile(p, []byte(`{"schema":4,"mode":"rule","selected":"香港1","lineAuto":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(p); err != nil || got.Selected != "香港1" || got.SelectedNode != "" {
		t.Fatalf("前一版的写法: %+v %v", got.Selected, err)
	}
	// 命令行改不了这两个(不是设置项)
	if _, err := Default().Apply("selectedGroup", "x"); err == nil {
		t.Fatal("selectedGroup 不是给人改的")
	}
}
