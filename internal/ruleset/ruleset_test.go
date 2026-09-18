package ruleset

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这个包存在的理由只有一条:**规则集在任何情况下都不许拖垮内核启动**。
// 2026-09-18 用户的索尼电视上,三个规则集全靠内核启动时现场从 GitHub 下,下不动,
// 于是 box.Start() 整个失败,界面只剩一句「内核启动失败」,全局和规则模式都连不上。
// 下面每条测试都是那次的一个切面。

func TestBuiltinAreRealRuleSets(t *testing.T) {
	names := Builtin()
	if len(names) < 3 {
		t.Fatalf("内置规则集少了:%v", names)
	}
	want := map[string]bool{"geosite-cn": false, "geoip-cn": false, "geosite-category-ads-all": false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
		// 默认设置一定会用到这几个,内容必须是内核读得动的
		if err := Valid(Bytes(n)); err != nil {
			t.Fatalf("内置的 %s 内核读不动:%v —— 装出去就是一台连不上的机器", n, err)
		}
	}
	for n, ok := range want {
		if !ok {
			t.Fatalf("默认配置要用的 %s 不在内置清单里:%v", n, names)
		}
	}
}

func TestInstallIsIdempotentAndRepairs(t *testing.T) {
	root := t.TempDir()
	if err := Install(root); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, subBuiltin, "geosite-cn.srs")
	st1, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(root); err != nil { // 第二次不该重写
		t.Fatal(err)
	}
	st2, _ := os.Stat(p)
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatal("内容没变却重写了一遍")
	}

	// 文件被写坏(掉电、磁盘坏块、杀毒软件截断):下次铺的时候要修回来
	if err := os.WriteFile(p, []byte("坏掉的"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Install(root); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != string(Bytes("geosite-cn")) {
		t.Fatal("坏掉的内置规则集没被修回来")
	}
}

func TestFindPrefersUserThenDownloadedThenBuiltin(t *testing.T) {
	root := t.TempDir()
	if err := Install(root); err != nil {
		t.Fatal(err)
	}
	// 只有内置
	p, ok := Find(root, "geosite-cn")
	if !ok || !strings.Contains(filepath.ToSlash(p), "/"+subBuiltin+"/") {
		t.Fatalf("该用内置那份,实际 %q", p)
	}
	// 下载层盖过内置
	if err := Save(root, "geosite-cn", Bytes("geoip-cn")); err != nil {
		t.Fatal(err)
	}
	p, ok = Find(root, "geosite-cn")
	if !ok || !strings.Contains(filepath.ToSlash(p), "/"+subDownloaded+"/") {
		t.Fatalf("该用下载来的那份,实际 %q", p)
	}
	// 用户自己放的盖过一切
	if err := os.WriteFile(filepath.Join(root, "geosite-cn.srs"), Bytes("geosite-category-ads-all"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, ok = Find(root, "geosite-cn")
	if !ok || filepath.Dir(p) != root {
		t.Fatalf("该用用户自己放的那份,实际 %q", p)
	}
}

// 坏文件必须当作"没有":本地规则集读不出来和远程下不到是一样的后果 —— 内核整个起不来。
func TestFindSkipsAndRepairsBrokenFiles(t *testing.T) {
	root := t.TempDir()
	if err := Install(root); err != nil {
		t.Fatal(err)
	}
	dl := filepath.Join(root, subDownloaded)
	if err := os.MkdirAll(dl, 0o700); err != nil {
		t.Fatal(err)
	}
	half := Bytes("geosite-cn")[:120] // 下载下到一半就断了
	broken := filepath.Join(dl, "geosite-cn.srs")
	if err := os.WriteFile(broken, half, 0o600); err != nil {
		t.Fatal(err)
	}
	p, ok := Find(root, "geosite-cn")
	if !ok {
		t.Fatal("下载层是坏的,应该退回内置那份,而不是当作没有")
	}
	if !strings.Contains(filepath.ToSlash(p), "/"+subBuiltin+"/") {
		t.Fatalf("应该退回内置,实际 %q", p)
	}
	if _, err := os.Stat(broken); !os.IsNotExist(err) {
		t.Fatal("我们自己那层的坏文件应该顺手删掉,免得每次都撞一遍")
	}

	// 用户自己放的坏文件不删,只是跳过
	user := filepath.Join(root, "geosite-cn.srs")
	if err := os.WriteFile(user, []byte("这不是规则集"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, ok = Find(root, "geosite-cn")
	if !ok || !strings.Contains(filepath.ToSlash(p), "/"+subBuiltin+"/") {
		t.Fatalf("用户那份读不动时应退回内置,实际 %q ok=%v", p, ok)
	}
	if _, err := os.Stat(user); err != nil {
		t.Fatal("不该动用户自己放的文件")
	}
	if len(Broken()) == 0 {
		t.Fatal("坏文件的原因要记下来,否则用户无从查起")
	}
}

func TestFindMissing(t *testing.T) {
	if _, ok := Find(t.TempDir(), "geosite-nonesuch"); ok {
		t.Fatal("没有的规则集不该说有")
	}
	if _, ok := Find("", "geosite-cn"); ok {
		t.Fatal("根目录为空时不该说有")
	}
}

// 下载回来的东西必须校验后才落盘:被劫持成一页 HTML、或者半截文件,
// 落到盘上就等于给下一次启动埋一颗雷。
func TestFetchRefusesGarbage(t *testing.T) {
	root := t.TempDir()
	var body []byte
	var code int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code != 0 && code != http.StatusOK {
			w.WriteHeader(code)
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	cases := []struct {
		name string
		body []byte
		code int
	}{
		{"一页 HTML 错误提示", []byte("<html>404 not found</html>"), 200},
		{"空的", nil, 200},
		{"半截文件", Bytes("geosite-cn")[:90], 200},
		{"服务器报错", []byte("nope"), 500},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body, code = c.body, c.code
			err := Fetch(context.Background(), srv.Client(), root, "geosite-test", srv.URL)
			if err == nil {
				t.Fatal("这种内容不该被收下")
			}
			if _, statErr := os.Stat(filepath.Join(root, subDownloaded, "geosite-test.srs")); !os.IsNotExist(statErr) {
				t.Fatal("校验没过却落盘了")
			}
		})
	}

	t.Run("正常的收下", func(t *testing.T) {
		body, code = Bytes("geosite-cn"), 200
		if err := Fetch(context.Background(), srv.Client(), root, "geosite-test", srv.URL); err != nil {
			t.Fatal(err)
		}
		p, ok := Find(root, "geosite-test")
		if !ok || !strings.Contains(filepath.ToSlash(p), "/"+subDownloaded+"/") {
			t.Fatalf("下回来的应该能找到:%q", p)
		}
	})
}

// 标签会被拼进路径,而这个包按标签**删文件、写文件**。settings 那边今天挡住了斜杠,
// 但这是公开 API:门要放在最贴近危险动作的地方,不指望上游永远记得校验。
func TestTagCannotEscapeTheDirectory(t *testing.T) {
	root := t.TempDir()
	if err := Install(root); err != nil {
		t.Fatal(err)
	}
	// 目录外面放一个同名的受害者,证明它不会被碰
	outside := filepath.Join(filepath.Dir(root), "victim.srs")
	if err := os.WriteFile(outside, []byte("不是规则集,解不开"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := []string{
		"geosite-../../victim",
		"../victim",
		"geosite-a/b",
		"geosite-a" + string(filepath.Separator) + "b",
		"C:/Windows/System32/x",
		"",
		strings.Repeat("a", 200),
	}
	for _, tag := range bad {
		if p, ok := Find(root, tag); ok {
			t.Errorf("标签 %q 不该被接受,却找到了 %s", tag, p)
		}
		if err := Save(root, tag, Bytes("geosite-cn")); err == nil {
			t.Errorf("标签 %q 不该被接受,却写进去了", tag)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("目录外面的文件被动了: %v", err)
	}
	// 正常的标签照旧能用
	if _, ok := Find(root, "geosite-category-ads-all"); !ok {
		t.Fatal("正常标签被误伤了")
	}
}
