package ipc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 给页面的状态去掉凭据(密码哈希、订阅地址、续费地址),别的一样不少;原来那份不能被改(服务自己还要用)。
func TestStateViewForPage(t *testing.T) {
	s := settings.Default()
	s.WebPassword = "salt$hash"
	s.Profiles = []settings.Profile{{ID: "p1", Name: "一", URL: "https://x/sub/TOKEN"}}
	pv := ProfileView{ID: "p1", Name: "一", URL: "https://x/sub/TOKEN", WebPage: "https://x/renew/TOKEN", NodeCount: 3}
	v := StateView{Node: "hk", Settings: s, Profile: &pv, Profiles: []ProfileView{pv}}
	page := v.ForPage()
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "TOKEN") || strings.Contains(string(raw), "salt$hash") {
		t.Fatalf("给页面的状态里带着凭据: %s", raw)
	}
	if page.Node != "hk" || page.Profile.NodeCount != 3 || page.Profiles[0].Name != "一" || page.Settings.Profiles[0].ID != "p1" {
		t.Fatalf("别的都要留着: %s", raw)
	}
	if v.Settings.WebPassword != "salt$hash" || v.Settings.Profiles[0].URL == "" || v.Profile.URL == "" || v.Profiles[0].WebPage == "" {
		t.Fatal("原来那份被改了")
	}
}

// 订阅列表给页面的那份:去掉订阅地址和续费地址,别的照留;原来的列表不动(服务、订阅管理页还要用);nil 还是 nil。
func TestProfilesForPage(t *testing.T) {
	list := []ProfileView{{ID: "p1", Name: "一", URL: "https://x/sub/TOKEN", WebPage: "https://x/renew/TOKEN", NodeCount: 3, Refreshing: true, Error: "坏了"}}
	page := ProfilesForPage(list)
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "TOKEN") {
		t.Fatalf("订阅列表里带着地址: %s", raw)
	}
	if page[0].ID != "p1" || page[0].NodeCount != 3 || !page[0].Refreshing || page[0].Error != "坏了" {
		t.Fatalf("别的都要留着(刷新状态、错误页面要看): %s", raw)
	}
	if list[0].URL == "" || list[0].WebPage == "" {
		t.Fatal("原来那份被改了")
	}
	if ProfilesForPage(nil) != nil {
		t.Fatal("nil 还是 nil")
	}
}
