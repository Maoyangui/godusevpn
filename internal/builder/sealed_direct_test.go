package builder

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 严格全局下隧道以外只剩节点连接本身:节点服务器直连规则只放内核自己(SelfProcess),用户去往和节点同域名 / 同地址、
// 同端口的连接照常走隧道;按进程直连、网关模式里设成"直连"的设备不生效。节点规则的进程限定在普通全局下同样有;
// 规则模式不加。
func TestSealedDirectOnlyForNodeConnections(t *testing.T) {
	p := &profile.Profile{Tags: []string{"cdn"}, Outbounds: []json.RawMessage{
		json.RawMessage(`{"type":"vless","tag":"cdn","server":"www.visa.com","server_port":443,"uuid":"x","tls":{"enabled":true}}`),
	}}
	base := settings.Default()
	base.BypassApps = []string{"steam.exe"}
	base.NetMode = settings.NetGateway
	base.Devices = []settings.Device{{ID: "tv", IP: "192.168.1.50", Mode: "direct"}}

	var lastInbounds []map[string]any
	rulesOf := func(s settings.Settings, android bool) []map[string]any {
		t.Helper()
		raw, err := Build(Input{Profile: p, Settings: s, DataDir: t.TempDir(), ClashSecret: "sec", RuleSetDir: ruleSetRoot(t),
			NodeIPs: map[string][]string{"www.visa.com": {"104.18.20.30"}}, SelfProcess: "godusevpn-svc.exe", Android: android})
		if err != nil {
			t.Fatal(err)
		}
		var c cfg
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		lastInbounds = c.Inbounds
		return c.Route.Rules
	}
	excluded := func() bool {
		for _, in := range lastInbounds {
			if _, ok := in["exclude_package"]; ok {
				return true
			}
		}
		return false
	}
	nodeRules := func(rules []map[string]any) []map[string]any {
		var out []map[string]any
		for _, r := range rules {
			if r["outbound"] != "direct" {
				continue
			}
			if d, ok := r["domain"].([]any); ok && len(d) == 1 && d[0] == "www.visa.com" {
				out = append(out, r)
			}
			if c, ok := r["ip_cidr"].([]any); ok && len(c) == 1 && c[0] == "104.18.20.30/32" {
				out = append(out, r)
			}
		}
		return out
	}
	has := func(rules []map[string]any, key string) bool {
		for _, r := range rules {
			if _, ok := r[key]; ok && r["outbound"] == "direct" {
				return true
			}
		}
		return false
	}

	sealed := base
	sealed.Mode, sealed.NoDirect = settings.ModeGlobal, true
	rs := rulesOf(sealed, false)
	nr := nodeRules(rs)
	if len(nr) != 2 {
		t.Fatalf("节点域名与地址各一条直连规则,得到 %d 条: %v", len(nr), nr)
	}
	for _, r := range nr {
		if !reflect.DeepEqual(r["process_name"], []any{"godusevpn-svc.exe"}) {
			t.Fatalf("严格全局下节点直连规则要限定成内核自己的进程: %v", r)
		}
	}
	if has(rs, "source_ip_cidr") {
		t.Fatal("严格全局下设备的\"直连\"不该生效")
	}
	for _, r := range rs {
		if v, ok := r["process_path_regex"].([]any); ok && len(v) == 1 && strings.Contains(v[0].(string), "steam") {
			t.Fatal("严格全局下按进程直连不该生效")
		}
	}

	// Android:按本应用包名限定;按应用直连的应用也不再整个排除出 VPN
	androidRules := rulesOf(sealed, true)
	if excluded() {
		t.Fatal("严格全局下 Android 不该再把按应用直连的应用排除出 VPN")
	}
	if !func() bool { x := base; x.Mode = settings.ModeRule; rulesOf(x, true); return excluded() }() {
		t.Fatal("规则模式下 Android 的按应用直连应当照旧排除出 VPN")
	}
	for _, r := range nodeRules(androidRules) {
		if !reflect.DeepEqual(r["package_name"], []any{"godusevpn-svc.exe"}) || r["process_name"] != nil {
			t.Fatalf("Android 上按包名限定: %v", r)
		}
	}

	// 不是严格全局:按进程直连、设备直连都在;节点规则只在全局模式下限定进程(全局就是一切走代理),规则模式不限定
	for _, s := range []settings.Settings{
		func() settings.Settings { x := base; x.Mode = settings.ModeRule; return x }(),
		func() settings.Settings { x := base; x.Mode, x.NoDirect = settings.ModeGlobal, false; return x }(),
	} {
		rs := rulesOf(s, false)
		nr := nodeRules(rs)
		if len(nr) != 2 {
			t.Fatalf("节点域名与地址各一条直连规则(%s/%v),得到 %d 条", s.Mode, s.NoDirect, len(nr))
		}
		for _, r := range nr {
			limited := reflect.DeepEqual(r["process_name"], []any{"godusevpn-svc.exe"})
			if want := s.Mode == settings.ModeGlobal; limited != want || (!want && r["process_name"] != nil) {
				t.Fatalf("节点规则限定进程应为 %v(%s/%v): %v", want, s.Mode, s.NoDirect, r)
			}
		}
		if !has(rs, "source_ip_cidr") {
			t.Fatalf("非严格全局下设备直连应当照旧(%s/%v)", s.Mode, s.NoDirect)
		}
		found := false
		for _, r := range rs {
			if v, ok := r["process_path_regex"].([]any); ok && len(v) == 1 && strings.Contains(v[0].(string), "steam") {
				found = true
			}
		}
		if !found {
			t.Fatalf("非严格全局下按进程直连应当照旧(%s/%v)", s.Mode, s.NoDirect)
		}
	}

	// 普通全局下,用户打开和节点同域名 / 同地址、同端口的网站照样走代理,只有内核自己的连接直连
	x := base
	x.Mode, x.NoDirect, x.NetMode = settings.ModeGlobal, false, settings.NetLocal
	raw, err := Build(Input{Profile: p, Settings: x, DataDir: t.TempDir(), ClashSecret: "sec", RuleSetDir: ruleSetRoot(t),
		NodeIPs: map[string][]string{"www.visa.com": {"104.18.20.30"}}, SelfProcess: "godusevpn-svc.exe"})
	if err != nil {
		t.Fatal(err)
	}
	var c cfg
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	for _, cn := range []conn{
		{dst: "www.visa.com", answers: []string{"104.18.20.30"}, proc: "/opt/browser/chrome"},
		{dst: "104.18.20.30", proc: "/opt/browser/chrome"},
	} {
		if out, at := route(t, c, "Global", cn); out != "proxy" {
			t.Fatalf("普通全局下浏览器去 %s 应走代理,实际 %s(规则 %d)", cn.dst, out, at)
		}
	}
	if out, _ := route(t, c, "Global", conn{dst: "104.18.20.30", proc: "/opt/godusevpn/godusevpn-svc.exe"}); out != "direct" {
		t.Fatalf("内核自己连节点应直连,实际 %s", out)
	}
}
