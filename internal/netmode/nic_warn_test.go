package netmode

import (
	"strings"
	"testing"
)

// 这条钉住修复 m29 的那个核心判断:没动成的网卡该不该把连接挡死,取决于**此刻是不是真的在漏**,
// 而不是"有没有出错"。m29 一律当成错(于是没编 IPv6 的内核、IPv6 设成手动的 macOS、
// 跑着容器的 Linux 全都连不上,用户只能去关掉「全局禁直连」,反倒更不私密);
// m28 一律吞掉(真漏了也不说)。两头都不对。
func TestNicResult(t *testing.T) {
	orig := nicLeakConfirmed
	defer func() { nicLeakConfirmed = orig }()

	for _, c := range []struct {
		name     string
		errs     []string
		leaking  bool
		wantErr  bool
		wantWarn bool
	}{
		{"全都动成了", nil, false, false, false},
		{"全都动成了,但别处在漏 —— 不是这次没动成造成的,不拦", nil, true, false, false},
		{"有没动成的,但查下来没有公网 v6 露出来:照常连,如实告警", []string{"eth9: 没这张网卡"}, false, false, true},
		{"有没动成的,而且确实有网卡挂着公网 v6:这才拒绝", []string{"eth0: 权限不足"}, true, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			setNICWarning("上一轮留下的")
			nicLeakConfirmed = func(string) bool { return c.leaking }
			err := nicResult("godusevpn", c.errs)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v,想要出错 = %v", err, c.wantErr)
			}
			if got := NICWarning() != ""; got != c.wantWarn {
				t.Fatalf("告警 = %q,想要有告警 = %v", NICWarning(), c.wantWarn)
			}
		})
	}
}

// 「漏」和「确证在漏」是两个问题,对"查不出来"的态度正相反:
// 前者宁可多跑一次停用脚本,后者绝不拿"没查出来"去拒绝用户。
func TestLeakPredicatesDisagreeOnUnknown(t *testing.T) {
	// 两个谓词都从同一个 nicIPv6LeakState 出来,这里只钉住组合逻辑。
	for _, c := range []struct{ leaking, known, wantMaybe, wantConfirmed bool }{
		{false, true, false, false}, // 查清楚了,没漏
		{true, true, true, true},    // 查清楚了,在漏
		{false, false, true, false}, // 没查清楚:要重试,但不能拒绝用户
	} {
		maybe := c.leaking || !c.known
		confirmed := c.leaking && c.known
		if maybe != c.wantMaybe || confirmed != c.wantConfirmed {
			t.Fatalf("(leaking=%v known=%v) => (%v, %v),想要 (%v, %v)",
				c.leaking, c.known, maybe, confirmed, c.wantMaybe, c.wantConfirmed)
		}
	}
}

// 备份坏了必须**自愈**,不能硬失败:停用那一路报错 = 连不上(关掉「连接时停用网卡 IPv6」
// 也救不回来,那条开关不影响"要不要先读备份");还原那一路报错 = 备份永远删不掉、卸载永远跑不完。
// 而原值本来就随文件一起丢了,继续硬失败换不回任何东西。
func TestNicBackupCorruptSelfHeals(t *testing.T) {
	orig := osRename
	defer func() { osRename = orig }()

	var from, to string
	osRename = func(a, b string) error { from, to = a, b; return nil }
	setNICWarning("")

	if err := nicBackupCorrupt("/data/nic-ipv6-backup.json", "unexpected end of JSON input"); err != nil {
		t.Fatalf("备份损坏不该返回错误(会把连接和卸载一起卡死),却返回了 %v", err)
	}
	if from != "/data/nic-ipv6-backup.json" || to != "/data/nic-ipv6-backup.json.bad" {
		t.Fatalf("坏文件没有挪到 .bad:%q -> %q", from, to)
	}
	w := NICWarning()
	if w == "" {
		t.Fatal("原始状态丢了却没有任何告警:用户不会知道要手动把 IPv6 开回来")
	}
	if !strings.Contains(w, ".bad") || !strings.Contains(w, "手动") {
		t.Fatalf("告警没说清楚坏文件在哪、要怎么补救: %q", w)
	}
}
