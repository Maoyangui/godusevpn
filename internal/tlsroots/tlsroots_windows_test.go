//go:debug x509usefallbackroots=1

package tlsroots

import (
	"crypto/x509"
	"testing"
)

// 打开 x509usefallbackroots、import 本包之后,进程里"系统根证书"就是内置 + 本机那份普通证书池:Go 自己校验,
// 不再交给 Windows 的平台校验(网络变化时它偶尔"成功但不给链",Go 顺着空指针崩在 crypt32 里)。
func TestSystemRootsAreBundledPool(t *testing.T) {
	if !Active() {
		t.Fatalf("没生效:%s", Status())
	}
	p, err := x509.SystemCertPool()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(p.Subjects()); n < 100 { //nolint:staticcheck
		t.Fatalf("系统根证书池只有 %d 张:还是平台校验的空标记池,或者内置根证书没装上", n)
	}
	if mozilla < 100 {
		t.Fatalf("内置 Mozilla 根证书只有 %d 张", mozilla)
	}
	t.Logf("%s", Status())
}
