package daemon

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/state"
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
	d.settings.NoDirect, d.settings.Mode = true, settings.ModeGlobal
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
	d.settings.NoDirect, d.settings.Mode = true, settings.ModeGlobal
	d.guardOn = true
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := d.fetchProfile(ctx, "https://sub.example.invalid/x")
	if err == nil || !strings.Contains(err.Error(), "https://127.0.0.1:443/") {
		t.Fatalf("内核没跑时拉订阅应当经 DoH 解析,得到 %v", err)
	}
	// 错误文本会进界面、服务日志与诊断:订阅地址只留协议与主机(路径就是令牌)
	if strings.Contains(err.Error(), "sub.example.invalid/x") {
		t.Fatalf("拉订阅失败的错误里带着订阅路径: %v", err)
	}
}

// 服务自己访问外网(更新检查、下载安装包、补规则集):闸开着而内核没跑时不许从本机直连出去;闸没开照常。
// 闸开着且内核在跑时经代理出站(那一支要真起内核,由真机验收覆盖)。
func TestSelfHTTPRefusesDirectWhenGuardArmed(t *testing.T) {
	d := newPolicyTestDaemon(t)
	if c, err := d.SelfHTTP(time.Second); err != nil || c == nil {
		t.Fatalf("闸没开时应当照常给客户端:%v", err)
	}
	d.settings.NoDirect, d.settings.Mode = true, settings.ModeGlobal
	d.guardOn = true
	if c, err := d.SelfHTTP(time.Second); !errors.Is(err, errSealed) || c != nil {
		t.Fatalf("闸开着、内核没跑时不该给直连客户端:c=%v err=%v", c, err)
	}
}

// 接线:补规则集走 SelfHTTP —— 闸开着、内核没跑时报"不从本机直连出去",而不是去直连。
func TestFillMissingRuleSetsUsesSelfHTTP(t *testing.T) {
	d := newPolicyTestDaemon(t)
	d.settings.NoDirect, d.settings.Mode = true, settings.ModeGlobal
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
	d.settings.NoDirect, d.settings.Mode = true, settings.ModeGlobal
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
	d.settings.NoDirect, d.settings.Mode = true, settings.ModeGlobal
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

// 只有严格全局的闸才拦服务自己的直连动作:规则模式本来就有直连,更新检查、未连接时的测速和以前一样。
func TestGuardArmedOnlySealed(t *testing.T) {
	d := newPolicyTestDaemon(t)
	d.guardOn = true
	d.settings.NoDirect, d.settings.Mode = true, settings.ModeRule
	if d.guardArmed() {
		t.Fatal("规则模式的闸不该拦服务自己的直连")
	}
	d.settings.Mode = settings.ModeGlobal
	if !d.guardArmed() {
		t.Fatal("严格全局的闸要拦")
	}
	d.guardOn, d.guardArming, d.settings.Mode = false, true, settings.ModeRule
	if !d.guardArmed() {
		t.Fatal("正在装严格全局的闸时也要拦")
	}
}

// 关着 IPv6 时,服务自己不经代理发出的请求只走 IPv4:没连接时拉订阅(闸开没开两种)、手动检查更新 / 下载安装包 / 补规则集
// (SelfHTTP)、DoH 解析,地址写成 IPv6 的在拨号这一步就被拒(*net.AddrError,一个包都不发)。开着 IPv6 时照常去拨。
// 拨的都是本机回环上没人听的端口,不出本机。
func TestDirectRequestsIPv4OnlyWhenIPv6Off(t *testing.T) {
	d := newPolicyTestDaemon(t)
	d.http = d.plainHTTP(5 * time.Second) // NewWithOptions 里就是这样装的
	get := func(name string, c *http.Client, url string) error {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := c.Do(req)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("%s:回环 9 端口不该有人应答", name)
		}
		return err
	}
	clients := func() map[string]*http.Client {
		self, err := d.SelfHTTP(5 * time.Second)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]*http.Client{"directHTTP(闸没开)": d.directHTTP(), "SelfHTTP": self}
	}
	var ae *net.AddrError
	for _, allow6 := range []bool{false, true} {
		d.mu.Lock()
		d.settings.IPv6 = allow6
		d.mu.Unlock()
		for name, c := range clients() {
			err := get(name, c, "http://[::1]:9/")
			if !allow6 && !errors.As(err, &ae) {
				t.Fatalf("%s:关着 IPv6 还去拨 IPv6 地址了:%v", name, err)
			}
			if allow6 && errors.As(err, &ae) {
				t.Fatalf("%s:开着 IPv6 应当照常去拨:%v", name, err)
			}
		}
	}

	// 闸开着时那一份(按地址拨号、域名经 DoH)同样只拨 IPv4
	d.mu.Lock()
	d.settings.IPv6 = false
	d.settings.NoDirect, d.settings.Mode, d.settings.LocalDNS = true, settings.ModeGlobal, "127.0.0.1"
	d.guardOn = true
	d.mu.Unlock()
	if err := get("directHTTP(闸开着)", d.directHTTP(), "http://[::1]:9/"); !errors.As(err, &ae) {
		t.Fatalf("闸开着:关着 IPv6 还去拨 IPv6 地址了:%v", err)
	}

	// DoH 服务器自己写成 IPv6 地址:关着 IPv6 时拨号这一步就不去连它;「本地 DNS」填的是 IPv6 地址时换成按地址连的公共 DoH
	// (和配置里一致,见 builder.LocalDNS)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, qt := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		if _, err := queryDoH(ctx, "::1", "example.com", qt, false); !errors.As(err, &ae) {
			t.Fatalf("关着 IPv6 还去连 IPv6 地址的 DoH(%v):%v", qt, err)
		}
	}
	s := d.getSettings()
	s.LocalDNS = "2001:db8::53"
	if got := builder.LocalDNS(s); got != builder.SealedBootstrapDoH {
		t.Fatalf("关着 IPv6 时「本地 DNS」填 IPv6 地址:应换成 %s,得到 %s", builder.SealedBootstrapDoH, got)
	}
	s.IPv6 = true
	if got := builder.LocalDNS(s); got != "2001:db8::53" {
		t.Fatalf("开着 IPv6 时照用:得到 %s", got)
	}
}

// 界面、命令行等一次控制调用的上限不短于守护进程里最长的同步处理:连着时拉订阅的整条回退链,之后再备新配置、停旧起新(留一分钟)。
// 桌面端、命令行都按这个等(桌面端是 Windows / macOS 才编译的包,这里看源码)。
func TestCallersWaitAsLongAsDaemon(t *testing.T) {
	if need := fetchChainBudget() + state.PrepareTimeout + time.Minute; ipc.SlowCallTimeout < need {
		t.Fatalf("ipc.SlowCallTimeout=%v,守护进程最长要 %v", ipc.SlowCallTimeout, need)
	}
	b, err := os.ReadFile("../../cmd/godusevpn/app.go")
	if err != nil {
		t.Fatal(err)
	}
	body := funcBody(t, strings.ReplaceAll(string(b), "\r\n", "\n"), "func (a *App) call(method string, params, result any) error {")
	if !strings.Contains(body, "ipc.SlowCallTimeout") {
		t.Fatalf("桌面端调守护进程没按 ipc.SlowCallTimeout 等:\n%s", body)
	}
}
