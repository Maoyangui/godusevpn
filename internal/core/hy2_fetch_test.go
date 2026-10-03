//go:build with_quic

package core

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func selfSignedPEM(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}))
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

// 经 hysteria2 出站拉订阅:面板的订阅服务只说 HTTP/1.1、发完就关。正文要完整拿到 —— 用户日志里经 hysteria2 节点
// 拉订阅总是"读取订阅失败: EOF"(响应头到了、正文少了最后约 2 KB),经 anytls 节点、直连都正常。本机回环上起
// hysteria2 服务端复现:不还原 EOF(见 eofConn)时 20 次里有 5 ~ 8 次不完整。
func TestHTTPClientFullBodyOverHysteria2(t *testing.T) {
	body := strings.Repeat("0123456789abcdef", 32*1024) // 512 KB,和一份上百个节点的订阅一个量级
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i < len(body); i += 16 << 10 {
			_, _ = io.WriteString(w, body[i:i+16<<10])
		}
	}))
	srv.StartTLS() // 不开 HTTP/2,和订阅服务一样只说 HTTP/1.1
	defer srv.Close()

	cert, key := selfSignedPEM(t)
	port := freeUDPPort(t)
	cfg, _ := json.Marshal(map[string]any{
		"log": map[string]any{"level": "error"},
		"inbounds": []any{map[string]any{"type": "hysteria2", "tag": "hy-in", "listen": "127.0.0.1", "listen_port": port,
			"users": []any{map[string]any{"password": "p"}},
			"tls":   map[string]any{"enabled": true, "certificate": strings.Split(strings.TrimSpace(cert), "\n"), "key": strings.Split(strings.TrimSpace(key), "\n")}}},
		"outbounds": []any{
			map[string]any{"type": "hysteria2", "tag": "hy", "server": "127.0.0.1", "server_port": port, "password": "p",
				"tls": map[string]any{"enabled": true, "server_name": "localhost", "insecure": true}},
			map[string]any{"type": "direct", "tag": "direct"},
		},
		"route":        map[string]any{"final": "direct"},
		"experimental": map[string]any{"cache_file": map[string]any{"enabled": true, "path": cachePath(t)}},
	})
	c := New(nil)
	if err := c.Start(cfg); err != nil {
		t.Fatal(err)
	}
	defer c.Stop()
	cl, err := c.HTTPClient("hy", 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cl.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	time.Sleep(time.Second)
	for i := 0; i < 20; i++ {
		var resp *http.Response
		for try := 0; ; try++ { // 内核刚起来时网络监测会报"网络变化"、拆掉 QUIC 会话:请求没发出去就重来,和这里要测的无关
			if resp, err = cl.Get(srv.URL); err == nil || try == 5 {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("第 %d 次: %v", i+1, err)
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || len(b) != len(body) {
			t.Fatalf("第 %d 次正文不完整: 读到 %d / %d 字节, err=%v", i+1, len(b), len(body), err)
		}
	}
}
