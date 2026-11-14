package directnet

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNetwork(t *testing.T) {
	for _, c := range []struct {
		in     string
		allow6 bool
		want   string
	}{
		{"tcp", false, "tcp4"}, {"tcp6", false, "tcp4"}, {"tcp4", false, "tcp4"},
		{"udp", false, "udp4"}, {"udp6", false, "udp4"}, {"ip", false, "ip4"},
		{"unix", false, "unix"},
		{"tcp", true, "tcp"}, {"udp", true, "udp"}, {"tcp6", true, "tcp6"},
	} {
		if got := Network(c.in, c.allow6); got != c.want {
			t.Errorf("Network(%q, %v) = %q,要 %q", c.in, c.allow6, got, c.want)
		}
	}
}

// 每次拨号都重新问一次开关;没给开关按关着算。
func TestDialAsksEveryTime(t *testing.T) {
	var got []string
	fake := func(_ context.Context, network, _ string) (net.Conn, error) {
		got = append(got, network)
		return nil, errors.New("不真拨")
	}
	allow := false
	dial := Dial(fake, func() bool { return allow })
	_, _ = dial(context.Background(), "tcp", "example.com:443")
	allow = true
	_, _ = dial(context.Background(), "tcp", "example.com:443")
	_, _ = Dial(fake, nil)(context.Background(), "tcp", "example.com:443")
	if strings.Join(got, ",") != "tcp4,tcp,tcp4" {
		t.Fatalf("拨号网络 %v", got)
	}
}

// 关着 IPv6:写成 IPv6 的地址在拨号这一步就被拒(一个包都不发),错误不是"连接被拒";开着时照常去拨。
// 拨的是本机回环上没人听的端口,不出本机。
func TestTransportRefusesV6WhenOff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	off := Client(5*time.Second, func() bool { return false })
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://[::1]:9/", nil)
	_, err := off.Do(req)
	var ae *net.AddrError
	if err == nil || !errors.As(err, &ae) {
		t.Fatalf("关着 IPv6 还去拨 IPv6 地址了:%v", err)
	}
	on := Client(5*time.Second, func() bool { return true })
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, "http://[::1]:9/", nil)
	_, err = on.Do(req)
	if err == nil || errors.As(err, &ae) {
		t.Fatalf("开着 IPv6 应当照常去拨(端口没人听而失败),得到 %v", err)
	}
}

// 开关只在拨号时判:不留空闲连接,免得开着 IPv6 时建的连接关掉以后还被复用。
func TestTransportNoIdleConns(t *testing.T) {
	if !Transport(nil, nil).DisableKeepAlives {
		t.Fatal("Transport 留着空闲连接")
	}
}

// 解析器连 DNS 服务器同样收口:关着 IPv6 时 IPv6 地址的 DNS 服务器不去连(拨号这一步就被拒)。
func TestResolverDialsV4(t *testing.T) {
	r := Resolver((&net.Dialer{}).DialContext, func() bool { return false })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := r.Dial(ctx, "udp", "[::1]:53")
	var ae *net.AddrError
	if !errors.As(err, &ae) {
		t.Fatalf("关着 IPv6 还去连 IPv6 的 DNS 服务器:%v", err)
	}
	if r.PreferGo {
		t.Fatal("不该改成强制用 Go 的解析器(Windows、macOS 上行为会变)")
	}
}

func TestKeepV4(t *testing.T) {
	in := []string{"2001:db8::1", "1.2.3.4", "::ffff:5.6.7.8", "bad", " 9.9.9.9 "}
	if got := strings.Join(KeepV4(in, false), ","); got != "1.2.3.4,5.6.7.8,9.9.9.9" {
		t.Fatalf("KeepV4 = %s", got)
	}
	if got := KeepV4(in, true); len(got) != len(in) {
		t.Fatalf("开着 IPv6 应当原样返回:%v", got)
	}
}
