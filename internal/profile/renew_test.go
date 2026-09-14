package profile

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 续费地址挂在错误文本末尾的格式,两头(服务端拆、界面拆)都按它来,所以这里把往返钉死。
func TestRenewSuffix(t *testing.T) {
	msg := "面板不认识这条订阅链接(HTTP 404,x.example)"
	if got := WithRenew(msg, ""); got != msg {
		t.Fatalf("没有地址不该改文本:%q", got)
	}
	full := WithRenew(msg, "https://x.example/buy")
	text, link := SplitRenew(full)
	if text != msg || link != "https://x.example/buy" {
		t.Fatalf("拆回来不对:%q %q", text, link)
	}
	if text, link := SplitRenew(msg); text != msg || link != "" {
		t.Fatalf("没挂地址的文本应原样返回:%q %q", text, link)
	}
	if text, link := SplitRenew("something [renew=https://a] tail"); text != "something [renew=https://a] tail" || link != "" {
		t.Fatalf("标记不在末尾就不算:%q %q", text, link)
	}
}

// 到期 / 用尽的人拉订阅拿到 404;面板把续费地址随 404 发下来,客户端要接住它。
func TestFetchBlockedCarriesRenew(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Profile-Web-Page-Url", "https://liumeiti.vip/services/airport-node")
		http.NotFound(w, r)
	}))
	defer srv.Close()
	_, err := Fetch(context.Background(), srv.URL+"/sub/abc", nil)
	var fe *FetchError
	if !errors.As(err, &fe) || fe.Status != 404 {
		t.Fatalf("应是 404 的 FetchError,得 %v", err)
	}
	if fe.WebPage != "https://liumeiti.vip/services/airport-node" {
		t.Fatalf("续费地址没接住:%q", fe.WebPage)
	}
	if _, link := SplitRenew(fe.Msg); link != fe.WebPage {
		t.Fatalf("错误文本末尾也该挂着地址:%q", fe.Msg)
	}
	// 地址不合法(非 http(s))就当没有,文本也不挂
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Profile-Web-Page-Url", "javascript:alert(1)")
		http.NotFound(w, r)
	}))
	defer bad.Close()
	_, err = Fetch(context.Background(), bad.URL+"/sub/abc", nil)
	if errors.As(err, &fe); fe.WebPage != "" {
		t.Fatalf("非法地址不该接:%q", fe.WebPage)
	}
	if _, link := SplitRenew(fe.Msg); link != "" {
		t.Fatalf("非法地址不该挂到文本上:%q", fe.Msg)
	}
}
