package daemon

import (
	"testing"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 方向决定了失败时该不该回滚。
//
// 收紧失败可以整份回滚 —— 用户没拿到更松的配置,不吃亏。
// 放宽失败要是也回滚,用户就再也关不掉「全局禁直连」——而他去关它,十有八九正是因为此刻
// 连不上、想先把网拿回来。m29 让两个方向都必须"重启成功"才算数,于是节点一连不上,
// 逃生口自己就锁死了:关不掉开关 → 闸不撤 → 没网 → 还是关不掉。
func TestPrivacyRelaxes(t *testing.T) {
	base := settings.Default() // NoDirect=true, Mode=Rule, IPv6=false, DisableNICIPv6=true, TUN=true
	strict := base
	strict.Mode = settings.ModeGlobal

	mut := func(s settings.Settings, f func(*settings.Settings)) settings.Settings {
		f(&s)
		return s
	}

	for _, c := range []struct {
		name       string
		prev, next settings.Settings
		want       bool
	}{
		{"关掉全局禁直连", base, mut(base, func(s *settings.Settings) { s.NoDirect = false }), true},
		{"从严格全局切回规则", strict, mut(strict, func(s *settings.Settings) { s.Mode = settings.ModeRule }), true},
		{"打开 IPv6", base, mut(base, func(s *settings.Settings) { s.IPv6 = true }), true},
		{"关掉连接时停用网卡 IPv6", base, mut(base, func(s *settings.Settings) { s.DisableNICIPv6 = false }), true},
		{"关掉 TUN", base, mut(base, func(s *settings.Settings) { s.TUN = false }), true},

		{"打开全局禁直连(收紧)", mut(base, func(s *settings.Settings) { s.NoDirect = false }), base, false},
		{"切到严格全局(收紧)", base, strict, false},
		{"关掉 IPv6(收紧)", mut(base, func(s *settings.Settings) { s.IPv6 = true }), base, false},
		{"什么隐私项都没动", base, base, false},
		{"只改了别的(节点)", base, mut(base, func(s *settings.Settings) { s.Selected = "hk01" }), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := privacyRelaxes(c.prev, c.next); got != c.want {
				t.Fatalf("privacyRelaxes = %v,想要 %v", got, c.want)
			}
		})
	}
}

// 运行中切模式:只有跨过严格全局边界(禁直连 + 全局)才整份重建;没跨过的新旧配置只差 default_mode,就地切。
// 隐私相关的其余开关一律整份重建,一项都不能省。
func TestNeedsPrivacyRebuild(t *testing.T) {
	mode := func(noDirect bool, m string) settings.Settings {
		s := settings.Default()
		s.NoDirect, s.Mode = noDirect, m
		return s
	}
	g, r, d := settings.ModeGlobal, settings.ModeRule, settings.ModeDirect
	for _, c := range []struct {
		name       string
		prev, next settings.Settings
		want       bool
	}{
		{"禁直连开:规则 → 全局(进严格全局)", mode(true, r), mode(true, g), true},
		{"禁直连开:全局 → 直连(出严格全局)", mode(true, g), mode(true, d), true},
		{"禁直连开:规则 ↔ 直连", mode(true, r), mode(true, d), false},
		{"禁直连关:规则 → 全局", mode(false, r), mode(false, g), false},
		{"禁直连关:全局 → 直连", mode(false, g), mode(false, d), false},
		{"规则模式下开关禁直连", mode(false, r), mode(true, r), true},
		{"打开 IPv6", mode(false, r), func() settings.Settings { s := mode(false, r); s.IPv6 = true; return s }(), true},
		{"关掉连接时停用网卡 IPv6", mode(false, r), func() settings.Settings { s := mode(false, r); s.DisableNICIPv6 = false; return s }(), true},
		{"关掉 TUN", mode(false, r), func() settings.Settings { s := mode(false, r); s.TUN = false; return s }(), true},
	} {
		if got := needsPrivacyRebuild(c.prev, c.next); got != c.want {
			t.Fatalf("%s:needsPrivacyRebuild = %v,想要 %v", c.name, got, c.want)
		}
	}
}
