package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.4.3-m3", "0.3.0-m2", true}, {"0.3.0-m2", "0.4.3-m3", false}, {"1.0.0", "1.0.0-rc1", true}, {"1.0.0-rc1", "1.0.0", false}, {"v1.2.3", "1.2.3", false}, {"x", "1.0.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Fatalf("Newer(%s,%s)=%v", c.a, c.b, got)
		}
	}
}

// 接口 403(经代理出口限流)时要能从 Atom 订阅拿到版本,下载地址按 tag 拼,并 HEAD 确认安装包存在。
func TestCheckFallsBackToAtomOn403(t *testing.T) {
	want := assetName("0.4.4-m3") // 本平台的资产名(Windows 是安装包,Linux 是 tar.gz)
	var apiHits, atomHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api"):
			apiHits++
			w.WriteHeader(http.StatusForbidden)
		case r.URL.Path == "/releases.atom":
			atomHits++
			w.Header().Set("Content-Type", "application/atom+xml")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Release notes from godusevpn</title>
  <entry><id>tag:github.com,2008:Repository/1/v9.9.9-m9</id><title>v9.9.9-m9</title><link rel="alternate" type="text/html" href="https://github.com/Maoyangui/godusevpn/releases/tag/v9.9.9-m9"/><content type="html">&lt;p&gt;no installer&lt;/p&gt;</content></entry>
  <entry><id>tag:github.com,2008:Repository/1/v0.4.4-m3</id><title>v0.4.4-m3</title><link rel="alternate" type="text/html" href="https://github.com/Maoyangui/godusevpn/releases/tag/v0.4.4-m3"/><content type="html">&lt;p&gt;修了 &lt;b&gt;403&lt;/b&gt;&lt;/p&gt;</content></entry>
  <entry><id>tag:github.com,2008:Repository/1/v0.3.0-m2</id><title>v0.3.0-m2</title><link rel="alternate" type="text/html" href="https://github.com/Maoyangui/godusevpn/releases/tag/v0.3.0-m2"/><content type="html">old</content></entry>
</feed>`))
		case r.Method == http.MethodHead && r.URL.Path == "/download/v0.4.4-m3/"+want:
			w.Header().Set("Content-Length", "12345")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusNotFound) // 9.9.9 没有安装包
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	releasesAPI, releasesAtom, downloadBase = srv.URL+"/api", srv.URL+"/releases.atom", srv.URL+"/download/"

	rel, err := Check(context.Background(), "0.4.3-m3", true, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if rel == nil || rel.Version != "0.4.4-m3" || !rel.Prerelease {
		t.Fatalf("应从 Atom 拿到 0.4.4-m3(跳过没有安装包的 9.9.9): %+v", rel)
	}
	if rel.InstallerURL != srv.URL+"/download/v0.4.4-m3/"+want || rel.SumsURL != srv.URL+"/download/v0.4.4-m3/SHA256SUMS" || rel.Size != 12345 {
		t.Fatalf("下载地址 / 大小不对: %+v", rel)
	}
	if rel.Notes != "修了 403" {
		t.Fatalf("说明应去掉 HTML 标签: %q", rel.Notes)
	}
	if apiHits != 1 || atomHits != 1 {
		t.Fatalf("接口与 Atom 各应请求一次: %d %d", apiHits, atomHits)
	}

	// 不算预发布时,带后缀的 tag 都跳过 → 没有更新
	rel, err = Check(context.Background(), "0.4.3-m3", false, srv.Client())
	if err != nil || rel != nil {
		t.Fatalf("不含预发布时应为 nil: %v %v", rel, err)
	}
	// 已是最新
	rel, err = Check(context.Background(), "0.4.4-m3", true, srv.Client())
	if err != nil || rel != nil {
		t.Fatalf("已是最新时应为 nil: %v %v", rel, err)
	}
}

func TestCheckBothFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer srv.Close()
	releasesAPI, releasesAtom, downloadBase = srv.URL+"/api", srv.URL+"/releases.atom", srv.URL+"/download/"
	_, err := Check(context.Background(), "0.1.0", true, srv.Client())
	if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "备用通道") {
		t.Fatalf("两条路都失败时错误要说清楚: %v", err)
	}
}
