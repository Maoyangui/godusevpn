package daemon

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 禁直连下,服务自己直连拉订阅时的域名解析必须走 DoH(从本服务进程发出),不能交给系统解析器:
// Windows 上系统解析由 svchost 发包,闸不放行它(被"拦 DNS"挡下);放行时又是明文发给路由器。
func TestDirectHTTPUsesDoHWhenSealed(t *testing.T) {
	d := newPolicyTestDaemon(t)
	d.http = &http.Client{Timeout: 30 * time.Second}

	d.settings.NoDirect, d.settings.Mode = true, settings.ModeGlobal
	d.settings.LocalDNS = "127.0.0.1" // DoH 连不上:报错里要能看出走的是 DoH,而不是系统解析
	c := d.directHTTP()
	if c == d.http {
		t.Fatal("禁直连下还在用系统解析的客户端")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://sub.example.invalid/x", nil)
	_, err := c.Do(req)
	if err == nil || !strings.Contains(err.Error(), "经 DoH 解析 sub.example.invalid") {
		t.Fatalf("应当经 DoH 解析(并因为 127.0.0.1 上没有 DoH 而失败),得到 %v", err)
	}

	d.settings.LocalDNS = "system"
	if d.directHTTP() != d.http {
		t.Fatal("「本地 DNS」是 system 时没有加密解析可用,应当照旧")
	}
	d.settings.LocalDNS = "127.0.0.1"
	d.settings.Mode = settings.ModeRule
	if d.directHTTP() != d.http {
		t.Fatal("不是全局模式时行为不该变")
	}
	d.settings.Mode, d.settings.NoDirect = settings.ModeGlobal, false
	if d.directHTTP() != d.http {
		t.Fatal("禁直连关着时行为不该变")
	}
}

// 接线:内核没跑时拉订阅要用 directHTTP(禁直连下经 DoH 解析)。
func TestFetchProfileWithoutCoreUsesDirectHTTP(t *testing.T) {
	d := newPolicyTestDaemon(t)
	d.http = &http.Client{Timeout: 30 * time.Second}
	d.settings.NoDirect, d.settings.Mode, d.settings.LocalDNS = true, settings.ModeGlobal, "127.0.0.1"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := d.fetchProfile(ctx, "https://sub.example.invalid/x")
	if err == nil || !strings.Contains(err.Error(), "经 DoH 解析") {
		t.Fatalf("内核没跑时拉订阅应当经 DoH 解析,得到 %v", err)
	}
}
