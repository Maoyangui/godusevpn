package daemon

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// 闸开着时,服务自己直连拉订阅的域名解析必须走 DoH(从本服务进程发出),不能交给系统解析器:
// Windows 上系统解析由 svchost 发包,闸不放行它(被"拦 DNS"挡下);Linux / macOS 上守护进程是 root,闸放行它,
// 明文查询会直接出去。闸没开时行为不变。
func TestDirectHTTPUsesDoHWhenGuardArmed(t *testing.T) {
	old := sealedDoH
	sealedDoH = "127.0.0.1" // 替身也换成连不上的地址:报错里要能看出走的是 DoH,而不是系统解析
	t.Cleanup(func() { sealedDoH = old })

	d := newPolicyTestDaemon(t)
	d.http = &http.Client{Timeout: 30 * time.Second}
	d.settings.NoDirect, d.settings.Mode = true, settings.ModeGlobal

	if d.directHTTP() != d.http {
		t.Fatal("闸没开时行为不该变")
	}
	d.guardOn = true
	for _, local := range []string{"127.0.0.1", "system", "dns.example.invalid"} {
		d.settings.LocalDNS = local
		c := d.directHTTP()
		if c == d.http {
			t.Fatalf("「本地 DNS」=%s:闸开着还在用系统解析的客户端", local)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://sub.example.invalid/x", nil)
		_, err := c.Do(req)
		cancel()
		if err == nil || !strings.Contains(err.Error(), "经 DoH 解析 sub.example.invalid") || !strings.Contains(err.Error(), "127.0.0.1:443/dns-query") {
			t.Fatalf("「本地 DNS」=%s:应当向 127.0.0.1 发 DoH(并因为那里没有 DoH 而失败),得到 %v", local, err)
		}
	}
}

// 替身和生成配置时用的是同一台。
func TestSealedDoHMatchesBuilder(t *testing.T) {
	if sealedDoH != builder.SealedBootstrapDoH {
		t.Fatalf("sealedDoH=%s,生成配置用的是 %s", sealedDoH, builder.SealedBootstrapDoH)
	}
}

// 接线:内核没跑时拉订阅要用 directHTTP。
func TestFetchProfileWithoutCoreUsesDirectHTTP(t *testing.T) {
	d := newPolicyTestDaemon(t)
	d.http = &http.Client{Timeout: 30 * time.Second}
	d.settings.NoDirect, d.settings.Mode, d.settings.LocalDNS = true, settings.ModeGlobal, "127.0.0.1"
	d.guardOn = true
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := d.fetchProfile(ctx, "https://sub.example.invalid/x")
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:443/dns-query") {
		t.Fatalf("内核没跑时拉订阅应当经 DoH 解析,得到 %v", err)
	}
}

// 服务自己访问外网(更新检查、下载安装包、补规则集):闸开着而内核没跑时不许从本机直连出去;闸没开照常。
// 闸开着且内核在跑时经代理出站(那一支要真起内核,由真机验收覆盖)。
func TestSelfHTTPRefusesDirectWhenGuardArmed(t *testing.T) {
	d := newPolicyTestDaemon(t)
	if c, err := d.SelfHTTP(time.Second); err != nil || c == nil {
		t.Fatalf("闸没开时应当照常给客户端:%v", err)
	}
	d.guardOn = true
	if c, err := d.SelfHTTP(time.Second); !errors.Is(err, errSealed) || c != nil {
		t.Fatalf("闸开着、内核没跑时不该给直连客户端:c=%v err=%v", c, err)
	}
}

// 接线:补规则集走 SelfHTTP —— 闸开着、内核没跑时报"不从本机直连出去",而不是去直连。
func TestFillMissingRuleSetsUsesSelfHTTP(t *testing.T) {
	d := newPolicyTestDaemon(t)
	d.guardOn = true
	d.missingSets = []builder.MissingRuleSet{{Tag: "geosite-zz-test", URL: "https://rules.example.invalid/zz.srs"}}
	d.fillMissingRuleSets() // 里面先等 3 秒
	b, err := os.ReadFile(d.log.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "geosite-zz-test") || !strings.Contains(string(b), errSealed.Error()) {
		t.Fatalf("补规则集应当被闸挡住、不直连,日志:\n%s", b)
	}
}

// 闸没开时给的客户端不是一次定终身:闸开了以后的新请求被拒;读到一半闸开了,剩下的不再读。
func TestSelfHTTPStopsWhenGuardArmsMidway(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = io.WriteString(w, "world")
	}))
	defer srv.Close()
	defer close(release)

	d := newPolicyTestDaemon(t)
	c, err := d.SelfHTTP(10 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("闸没开时应当照常直连: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 5)
	if _, err := io.ReadFull(resp.Body, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("前半截: %q %v", buf, err)
	}

	d.mu.Lock()
	d.guardOn = true
	d.mu.Unlock()
	if _, err := resp.Body.Read(buf); !errors.Is(err, errArmedMidway) {
		t.Fatalf("闸开了还在隧道外接着读: %v", err)
	}
	if _, err := c.Get(srv.URL); err == nil || !strings.Contains(err.Error(), errArmedMidway.Error()) {
		t.Fatalf("闸开了以后同一个客户端的新请求应当被拒: %v", err)
	}
}

// 未连接时的测速跑到一半闸开了(用户点了连接):cancelOnGuard 要把它取消。
func TestCancelOnGuard(t *testing.T) {
	d := newPolicyTestDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := d.cancelOnGuard(ctx, cancel)
	defer stop()
	select {
	case <-ctx.Done():
		t.Fatal("闸没开不该取消")
	case <-time.After(300 * time.Millisecond):
	}
	d.mu.Lock()
	d.guardOn = true
	d.mu.Unlock()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("闸开了 2 秒还没取消")
	}
}

// 装闸之前(applyGuard 开头调 cancelDirect)要同步取消登记着的直连动作;注销过的不再被取消。
func TestCancelDirectCancelsHeld(t *testing.T) {
	d := newPolicyTestDaemon(t)
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	releaseA := d.holdDirect(cancelA)
	defer releaseA()
	releaseB := d.holdDirect(cancelB)
	releaseB() // 已经做完的
	d.cancelDirect()
	if ctxA.Err() == nil {
		t.Fatal("登记着的直连动作没被取消")
	}
	if ctxB.Err() != nil {
		t.Fatal("已注销的不该再被取消")
	}
}
