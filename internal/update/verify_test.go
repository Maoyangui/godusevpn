package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 这个更新器是以管理员身份静默安装的,所以"没校验成功"必须和"校验失败"一样对待。
// 早先三个洞叠在一起,效果就是"拿不到校验和 = 当作校验通过":fetchSum 不看 HTTP 状态码、
// 文件名找不到也返回空串且不报错、下载那边又写成 `want != "" && got != want`。
func TestDownloadRefusesWithoutGoodChecksum(t *testing.T) {
	payload := []byte("pretend this is an installer")
	sum := sha256.Sum256(payload)
	good := hex.EncodeToString(sum[:])

	var sumsBody string
	var sumsCode int
	pub, priv, _ := ed25519.GenerateKey(nil)
	oldPublicKey := releasePublicKey
	releasePublicKey = pub
	t.Cleanup(func() { releasePublicKey = oldPublicKey })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/SHA256SUMS.sig") {
			if sumsCode != http.StatusOK {
				w.WriteHeader(sumsCode)
				return
			}
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(sumsBody)))))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/SHA256SUMS") {
			if sumsCode != 0 && sumsCode != http.StatusOK {
				w.WriteHeader(sumsCode)
				_, _ = w.Write([]byte("<html>404 not found</html>"))
				return
			}
			_, _ = w.Write([]byte(sumsBody))
			return
		}
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	name := assetName("9.9.9")
	run := func(t *testing.T, body string, code int, sumsURL string) (string, error) {
		t.Helper()
		sumsBody, sumsCode = body, code
		dir := t.TempDir()
		rel := &Release{Version: "9.9.9", InstallerURL: srv.URL + "/" + name, SumsURL: sumsURL, SumsSigURL: srv.URL + "/SHA256SUMS.sig"}
		return Download(context.Background(), rel, dir, srv.Client(), nil)
	}
	gone := func(t *testing.T, dir string) {
		t.Helper()
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			t.Fatalf("校验没通过就该把下载的文件删掉,却还留着 %s", filepath.Join(dir, e.Name()))
		}
	}

	t.Run("校验和对得上就装", func(t *testing.T) {
		p, err := run(t, good+"  "+name+"\n", 200, srv.URL+"/SHA256SUMS")
		if err != nil {
			t.Fatalf("不该报错: %v", err)
		}
		if b, _ := os.ReadFile(p); string(b) != string(payload) {
			t.Fatal("下载的内容不对")
		}
	})

	t.Run("校验和对不上就拒装并删文件", func(t *testing.T) {
		p, err := run(t, strings.Repeat("0", 64)+"  "+name+"\n", 200, srv.URL+"/SHA256SUMS")
		if err == nil {
			t.Fatal("校验和对不上却装了")
		}
		if p != "" {
			t.Fatalf("出错时不该返回路径: %s", p)
		}
	})

	t.Run("SHA256SUMS 取不到就拒装", func(t *testing.T) {
		dir := t.TempDir()
		sumsBody, sumsCode = "", http.StatusNotFound
		rel := &Release{Version: "9.9.9", InstallerURL: srv.URL + "/" + name, SumsURL: srv.URL + "/SHA256SUMS", SumsSigURL: srv.URL + "/SHA256SUMS.sig"}
		if _, err := Download(context.Background(), rel, dir, srv.Client(), nil); err == nil {
			t.Fatal("404 的 SHA256SUMS 被当成了「没有校验和」,照装了")
		} else if !strings.Contains(err.Error(), "404") {
			t.Fatalf("错误里应带上 HTTP 状态:%v", err)
		}
		gone(t, dir)
	})

	t.Run("列表里没有这个文件名就拒装", func(t *testing.T) {
		dir := t.TempDir()
		sumsBody, sumsCode = good+"  某个别的文件.exe\n", 200
		rel := &Release{Version: "9.9.9", InstallerURL: srv.URL + "/" + name, SumsURL: srv.URL + "/SHA256SUMS", SumsSigURL: srv.URL + "/SHA256SUMS.sig"}
		if _, err := Download(context.Background(), rel, dir, srv.Client(), nil); err == nil {
			t.Fatal("文件名不在列表里被当成了「不需要校验」,照装了")
		}
		gone(t, dir)
	})

	t.Run("压根没发布校验和文件就拒装", func(t *testing.T) {
		dir := t.TempDir()
		rel := &Release{Version: "9.9.9", InstallerURL: srv.URL + "/" + name, SumsURL: ""}
		if _, err := Download(context.Background(), rel, dir, srv.Client(), nil); err == nil {
			t.Fatal("没有校验和文件也照装了")
		}
		gone(t, dir)
	})

	t.Run("缺少签名清单一律拒装", func(t *testing.T) {
		sumsBody, sumsCode = good+"  "+name+"\n", 200
		dir := t.TempDir()
		rel := &Release{Version: "9.9.9", InstallerURL: srv.URL + "/" + name, SumsURL: srv.URL + "/SHA256SUMS"}
		if _, err := Download(context.Background(), rel, dir, srv.Client(), nil); err == nil {
			t.Fatal("缺少签名清单的发布不应按 SHA256 降级安装")
		}
		gone(t, dir)
	})

	t.Run("未知版本即使 SHA256 正确也拒绝无签名", func(t *testing.T) {
		sumsBody, sumsCode = good+"  "+name+"\n", 200
		dir := t.TempDir()
		rel := &Release{Version: "0.6.25-m29", InstallerURL: srv.URL + "/" + name, SumsURL: srv.URL + "/SHA256SUMS"}
		if _, err := Download(context.Background(), rel, dir, srv.Client(), nil); err == nil {
			t.Fatal("未知版本不能用正确 SHA256 绕过签名")
		}
		gone(t, dir)
	})
}

func TestReleasePublicKeyIsEd25519TrustRoot(t *testing.T) {
	if len(releasePublicKey) != ed25519.PublicKeySize {
		t.Fatalf("发布公钥长度错误: got %d want %d", len(releasePublicKey), ed25519.PublicKeySize)
	}
	const want = "Cm9y5DPCEThof6VYSKwphCcowlFayLVniJ13cMWwKu4="
	if got := base64.StdEncoding.EncodeToString(releasePublicKey); got != want {
		t.Fatalf("发布公钥与 CI 信任根不一致: got %s want %s", got, want)
	}
}
