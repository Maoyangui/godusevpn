package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/nodelink"
	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/profile"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

func manualDaemon(t *testing.T) *Daemon {
	t.Helper()
	t.Setenv("GODUSEVPN_DATA", t.TempDir())
	if err := os.MkdirAll(paths.DataDir()+"/profiles", 0o700); err != nil {
		t.Fatal(err)
	}
	return profileOpsDaemon(t)
}

func manualNode(t *testing.T, link string) map[string]any {
	t.Helper()
	n, err := nodelink.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestManualNodesLifecycle(t *testing.T) {
	d := manualDaemon(t)
	if v := d.manualView(); v.ID != "" || len(v.Nodes) != 0 {
		t.Fatalf("一开始没有: %+v", v)
	}

	// 第一个节点:建出手动订阅,没有别的订阅时自动成为当前
	views, err := d.saveManualNodes([]map[string]any{manualNode(t, "trojan://pw@a.example.com:443#甲")}, "", "我的节点")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Name != "我的节点" || !views[0].Active || !views[0].Manual || views[0].NodeCount != 1 || views[0].URL != "" {
		t.Fatalf("%+v", views)
	}
	s := d.getSettings()
	if err := s.Validate(); err != nil {
		t.Fatalf("没有地址的手动订阅要能通过校验: %v", err)
	}

	// 再添加:同名的自动改名,不新建第二条订阅
	views, err = d.saveManualNodes([]map[string]any{manualNode(t, "hy2://pw@b.example.com:443#甲"), manualNode(t, "anytls://pw@c.example.com:443#丙")}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].NodeCount != 3 {
		t.Fatalf("%+v", views)
	}
	_, cur := d.manualProfile()
	if strings.Join(cur.Tags, ",") != "甲,甲 2,丙" {
		t.Fatalf("撞名要改开: %v", cur.Tags)
	}
	if !cur.Manual || cur.Stale(0) {
		t.Fatal("手动节点永远不算过期,不去拉")
	}

	// 编辑:位置不变;手动选着的节点改名后跟过去
	s = d.getSettings()
	s.Selected = "甲 2"
	if err := d.setSettings(s); err != nil {
		t.Fatal(err)
	}
	if _, err := d.saveManualNodes([]map[string]any{manualNode(t, "hy2://new@b2.example.com:8443#乙")}, "甲 2", ""); err != nil {
		t.Fatal(err)
	}
	_, cur = d.manualProfile()
	if strings.Join(cur.Tags, ",") != "甲,乙,丙" {
		t.Fatalf("编辑要留在原位: %v", cur.Tags)
	}
	if !strings.Contains(string(cur.Outbounds[1]), "b2.example.com") {
		t.Fatalf("%s", cur.Outbounds[1])
	}
	if got := d.getSettings().Selected; got != "乙" {
		t.Fatalf("选着的节点改名要跟过去: %q", got)
	}

	// 编辑不存在的、一次编辑多个
	if _, err := d.saveManualNodes([]map[string]any{manualNode(t, "hy2://p@x.example.com#x")}, "没有", ""); err == nil {
		t.Fatal("编辑不存在的节点要报错")
	}
	if _, err := d.saveManualNodes([]map[string]any{manualNode(t, "hy2://p@x.example.com#x"), manualNode(t, "hy2://p@y.example.com#y")}, "甲", ""); err == nil {
		t.Fatal("编辑一次只能一个")
	}
	if _, err := d.saveManualNodes(nil, "", ""); err == nil {
		t.Fatal("空的要报错")
	}

	// 删:正用着的节点被删回到自动
	if _, err := d.removeManualNode("乙"); err != nil {
		t.Fatal(err)
	}
	if d.getSettings().Selected != "" {
		t.Fatal("删掉正用的节点要回到自动选择")
	}
	if _, err := d.removeManualNode("没有"); err == nil {
		t.Fatal("删不存在的要报错")
	}

	// 刷新是空操作,不报错、不改内容
	sp, _ := d.manualProfile()
	changed, err := d.refreshProfile(context.Background(), sp.ID)
	if err != nil || changed {
		t.Fatalf("手动节点不拉取: %v %v", changed, err)
	}

	// 删到不剩:订阅也没了
	if _, err := d.removeManualNode("甲"); err != nil {
		t.Fatal(err)
	}
	views, err = d.removeManualNode("丙")
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 0 || d.manualView().ID != "" {
		t.Fatalf("最后一个删了订阅也要没: %+v", views)
	}
	if d.getSettings().ActiveProfile != "" {
		t.Fatal("删光了不该还指着它")
	}
}

// 同一批链接再贴一次不翻倍;名字不同、内容相同也算重复;全重复时要说一声。
func TestManualSkipsDuplicates(t *testing.T) {
	d := manualDaemon(t)
	a, b := "trojan://p@a.example.com#a", "trojan://p@b.example.com#b"
	if _, err := d.saveManualNodes([]map[string]any{manualNode(t, a), manualNode(t, b), manualNode(t, a)}, "", ""); err != nil {
		t.Fatal(err)
	}
	_, cur := d.manualProfile()
	if len(cur.Tags) != 2 {
		t.Fatalf("同一批里的重复也要跳过: %v", cur.Tags)
	}
	if _, err := d.saveManualNodes([]map[string]any{manualNode(t, a), manualNode(t, "trojan://p@c.example.com#c")}, "", ""); err != nil {
		t.Fatal(err)
	}
	_, cur = d.manualProfile()
	if strings.Join(cur.Tags, ",") != "a,b,c" {
		t.Fatalf("%v", cur.Tags)
	}
	_, err := d.saveManualNodes([]map[string]any{manualNode(t, "trojan://p@a.example.com#另一个名字")}, "", "")
	var ce *ipc.CallError
	if !errors.As(err, &ce) || ce.Code != "E_NODE_DUP" {
		t.Fatalf("全是重复的要报错: %v", err)
	}
	// 编辑不算重复:只改名字要能存
	if _, err := d.saveManualNodes([]map[string]any{manualNode(t, "trojan://p@a.example.com#新名字")}, "a", ""); err != nil {
		t.Fatal(err)
	}
}

// 编辑成和别的节点一样的名字:让开的是被编辑的这个,没动过的节点(可能正选着)名字不变。
func TestManualEditNameCollisionKeepsOthers(t *testing.T) {
	d := manualDaemon(t)
	if _, err := d.saveManualNodes([]map[string]any{manualNode(t, "trojan://p@a.example.com#a"), manualNode(t, "trojan://p@b.example.com#b")}, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.saveManualNodes([]map[string]any{manualNode(t, "trojan://p@a2.example.com#b")}, "a", ""); err != nil {
		t.Fatal(err)
	}
	_, cur := d.manualProfile()
	if strings.Join(cur.Tags, ",") != "b 2,b" {
		t.Fatalf("%v", cur.Tags)
	}
	if !strings.Contains(string(cur.Outbounds[0]), "a2.example.com") || !strings.Contains(string(cur.Outbounds[1]), "b.example.com") {
		t.Fatalf("内容对错了: %s", cur.Outbounds)
	}
	// 保存时名字没改:不和自己撞
	if _, err := d.saveManualNodes([]map[string]any{manualNode(t, "trojan://p@b3.example.com#b")}, "b", ""); err != nil {
		t.Fatal(err)
	}
	_, cur = d.manualProfile()
	if strings.Join(cur.Tags, ",") != "b 2,b" {
		t.Fatalf("和自己不算撞名: %v", cur.Tags)
	}
}

// 旧节点建不起来(比如升级后内核不认了)不能挡住添加新节点。
func TestManualOldBadNodeDoesNotBlock(t *testing.T) {
	d := manualDaemon(t)
	if _, err := d.saveManualNodes([]map[string]any{manualNode(t, "trojan://p@a.example.com#a")}, "", ""); err != nil {
		t.Fatal(err)
	}
	sp, cur := d.manualProfile()
	cur.Outbounds = append(cur.Outbounds, json.RawMessage(`{"type":"shadowsocks","tag":"旧","server":"o.example.com","server_port":1,"method":"not-a-method","password":"p"}`))
	cur.Tags = append(cur.Tags, "旧")
	d.setProfileCache(sp.ID, cur)
	if _, err := d.saveManualNodes([]map[string]any{manualNode(t, "trojan://p@n.example.com#新")}, "", ""); err != nil {
		t.Fatalf("旧节点坏了不该挡新节点: %v", err)
	}
}

// 重启后还在:缓存就是存储。设置和缓存都能读回来,还是手动节点。
func TestManualSurvivesRestart(t *testing.T) {
	d := manualDaemon(t)
	if _, err := d.saveManualNodes([]map[string]any{manualNode(t, "trojan://p@a.example.com#a")}, "", ""); err != nil {
		t.Fatal(err)
	}
	sp, _ := d.manualProfile()
	info, err := os.Stat(paths.ProfileCache(sp.ID))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("里面有密码,只能给 root 读: %v", info.Mode())
	}
	c, err := profile.Load(paths.ProfileCache(sp.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Manual || len(c.Tags) != 1 || c.Stale(0) {
		t.Fatalf("%+v", c)
	}
	// 缓存没了:连接时要报错而不是空指针
	d.mu.Lock()
	delete(d.profiles, sp.ID)
	d.mu.Unlock()
	if _, err := d.refreshProfile(context.Background(), sp.ID); err == nil {
		t.Fatal("节点没了要报错")
	}
}

// 走控制口:解析不落盘;错误带码;手动订阅不能被设成有地址的。
func TestManualOverIPC(t *testing.T) {
	d := manualDaemon(t)
	raw, _ := json.Marshal(map[string]string{"text": "hy2://p@a.example.com#a\nhttps://x.example.com/sub"})
	res, err := d.Dispatch(ipc.MParseNodeLinks, raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed := res.(ipc.ParsedNodes)
	if len(parsed.Nodes) != 1 || len(parsed.Errors) != 1 || parsed.Errors[0].Code != "subscription" || parsed.Errors[0].Line != 2 {
		t.Fatalf("%+v", parsed)
	}
	if len(d.getSettings().Profiles) != 0 {
		t.Fatal("解析不能落盘")
	}
	err = call(t, d, ipc.MSaveManualNodes, map[string]any{"nodes": []map[string]any{{"type": "trojan", "server": "h.example.com", "server_port": 443}}})
	var ce *ipc.CallError
	if !errors.As(err, &ce) || ce.Code != "E_NODE_PASSWORD" {
		t.Fatalf("要带错误码: %v", err)
	}
	if err := call(t, d, ipc.MSaveManualNodes, map[string]any{"nodes": parsed.Nodes, "name": "我的"}); err != nil {
		t.Fatal(err)
	}
	sp, _ := d.manualProfile()
	if err := call(t, d, ipc.MRenameProfile, map[string]string{"id": sp.ID, "url": "https://x.example.com/sub"}); err == nil {
		t.Fatal("手动节点没有地址")
	}
	if err := call(t, d, ipc.MRenameProfile, map[string]string{"id": sp.ID, "name": "改名"}); err != nil {
		t.Fatal(err)
	}
	if err := call(t, d, ipc.MRefreshProfile, map[string]any{"id": sp.ID}); err != nil {
		t.Fatalf("刷新手动节点是空操作: %v", err)
	}
	if err := call(t, d, ipc.MRemoveManualNode, map[string]string{"tag": "没有"}); !errors.As(err, &ce) || ce.Code != "E_NODE_GONE" {
		t.Fatalf("%v", err)
	}
	if got := d.getSettings().Profiles[0]; got.Name != "改名" || !got.Manual || got.URL != "" {
		t.Fatalf("%+v", got)
	}
}

// 内核不认的参数不能落盘:存进去这条订阅就连不上了。
func TestManualNodeRejectedByCore(t *testing.T) {
	d := manualDaemon(t)
	bad := map[string]any{"type": "shadowsocks", "server": "a.example.com", "server_port": 8388, "method": "not-a-method", "password": "pw", "tag": "坏节点"}
	_, err := d.saveManualNodes([]map[string]any{bad}, "", "")
	var ce *ipc.CallError
	if !errors.As(err, &ce) || ce.Code != "E_NODE_INVALID" || !strings.Contains(ce.Msg, "坏节点") {
		t.Fatalf("要指出是哪个节点: %v", err)
	}
	if v := d.manualView(); v.ID != "" {
		t.Fatalf("校验没过不能建订阅: %+v", v)
	}
	if len(d.getSettings().Profiles) != 0 {
		t.Fatal("校验没过不能改设置")
	}
}

// 每种协议解析出来的东西内核都要建得起来:链接解析和内核的字段对不上,这一步就露馅了。
func TestManualEveryProtocolBuilds(t *testing.T) {
	d := manualDaemon(t)
	const id = "11111111-1111-1111-1111-111111111111"
	vm := base64.StdEncoding.EncodeToString([]byte(`{"v":"2","ps":"vm","add":"vm.example.com","port":"443","id":"` + id + `","aid":"0","scy":"auto","net":"ws","host":"c.example.com","path":"/w","tls":"tls"}`))
	links := []string{
		"vless://" + id + "@a.example.com:443?security=reality&sni=www.apple.com&pbk=Ttnr8gLHFsOLbPh6ceTqeYcbOl9BrBCBqOSyH-fOazo&sid=ab&fp=chrome&flow=xtls-rprx-vision#vless-reality",
		"vless://" + id + "@a.example.com:443?security=tls&type=ws&path=%2Fa&host=c.example.com#vless-ws",
		"vless://" + id + "@a.example.com:443?security=tls&type=grpc&serviceName=s#vless-grpc",
		"vless://" + id + "@a.example.com:443?security=tls&type=httpupgrade&path=%2Fu&host=c.example.com#vless-hu",
		"vless://" + id + "@a.example.com:443?security=tls&type=http&path=%2Fh&host=c.example.com#vless-h2",
		"vmess://" + vm,
		"trojan://p@a.example.com:443?sni=s.example.com&type=ws&path=%2Ft#trojan",
		"ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:p")) + "@a.example.com:8388#ss",
		"ss://" + base64.RawURLEncoding.EncodeToString([]byte("2022-blake3-aes-128-gcm:AAAAAAAAAAAAAAAAAAAAAA==")) + "@a.example.com:8388#ss2022",
		"hysteria2://p@a.example.com:443?sni=s.example.com&obfs=salamander&obfs-password=o&mport=20000-30000&upmbps=50&downmbps=100#hy2",
		"tuic://" + id + ":p@a.example.com:443?congestion_control=bbr&udp_relay_mode=quic&reduce_rtt=1#tuic",
		"anytls://p@a.example.com:443?sni=s.example.com#anytls",
		"socks5://u:p@a.example.com:1080#socks",
		"http://u:p@a.example.com:3128#http",
		"https://u:p@a.example.com:443#https",
	}
	var nodes []map[string]any
	for _, l := range links {
		nodes = append(nodes, manualNode(t, l))
	}
	if _, err := d.saveManualNodes(nodes, "", ""); err != nil {
		t.Fatalf("内核建不起来: %v", err)
	}
	if v := d.manualView(); len(v.Nodes) != len(links) {
		t.Fatalf("%d", len(v.Nodes))
	}
}

// 当前是手动节点时命令行设订阅:加一条新的并切过去,手动节点留着。
func TestSetProfileURLWhileManualActive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(subBody)) }))
	defer srv.Close()
	d := manualDaemon(t)
	if _, err := d.saveManualNodes([]map[string]any{manualNode(t, "trojan://p@a.example.com#a")}, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := call(t, d, ipc.MSetProfileURL, map[string]string{"url": srv.URL + "/s"}); err != nil {
		t.Fatal(err)
	}
	s := d.getSettings()
	if len(s.Profiles) != 2 || !s.Profiles[0].Manual || s.Profiles[1].URL != srv.URL+"/s" || s.ActiveProfile != s.Profiles[1].ID {
		t.Fatalf("%+v", s.Profiles)
	}
}

// 闸开着时(规则模式也一样)服务自己拉订阅不能用没带放行标记的客户端:Linux 上会被自己的闸拦下,连接永远卡在拉订阅。
func TestDirectHTTPUnderAnyGuard(t *testing.T) {
	d := profileOpsDaemon(t)
	d.settings.Mode, d.settings.NoDirect, d.settings.TUN = settings.ModeRule, true, true
	if d.directHTTP() != d.http {
		t.Fatal("闸没开:照常用默认客户端")
	}
	d.guardOn = true
	if d.directHTTP() == d.http {
		t.Fatal("规则模式的闸开着:要用带放行标记的客户端")
	}
	d.settings.Mode = settings.ModeGlobal
	if d.directHTTP() == d.http {
		t.Fatal("严格全局的闸开着:要用带放行标记的客户端")
	}
	d.guardOn = false
	d.guardArming = true
	if d.directHTTP() == d.http {
		t.Fatal("正在装闸也算开着")
	}
}

// 手动订阅不要地址,普通订阅还是要。
func TestManualProfileValidate(t *testing.T) {
	s := settings.Default()
	s.Profiles = []settings.Profile{{ID: "x", Name: "n", Manual: true}}
	s.ActiveProfile = "x"
	if err := s.Validate(); err != nil {
		t.Fatalf("手动订阅不要地址: %v", err)
	}
	s.Profiles = []settings.Profile{{ID: "x", Name: "n"}}
	if err := s.Validate(); err == nil {
		t.Fatal("普通订阅还是要地址")
	}
}
