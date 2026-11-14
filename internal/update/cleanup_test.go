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
	"sort"
	"strconv"
	"strings"
	"testing"
)

// signedServer 起一个发布站:安装包 payload、SHA256SUMS 及其签名都对得上。
func signedServer(t *testing.T, version string, payload []byte) (*httptest.Server, *Release) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(nil)
	old := releasePublicKey
	releasePublicKey = pub
	t.Cleanup(func() { releasePublicKey = old })
	sum := sha256.Sum256(payload)
	name := assetName(version)
	sums := hex.EncodeToString(sum[:]) + "  " + name + "\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/SHA256SUMS.sig"):
			_, _ = w.Write([]byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(sums)))))
		case strings.HasSuffix(r.URL.Path, "/SHA256SUMS"):
			_, _ = w.Write([]byte(sums))
		default:
			_, _ = w.Write(payload)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &Release{Version: version, InstallerURL: srv.URL + "/" + name, SumsURL: srv.URL + "/SHA256SUMS", SumsSigURL: srv.URL + "/SHA256SUMS.sig"}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("old installer"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// 一台 Windows 电脑的 %TEMP%\godusevpn-update 里攒了 31 个旧安装包(0.6.3 到 0.7.16,约 500 MB):
// 每次升级都下一个新的,旧的从来不删。下新版之前,别的版本的安装包(和 macOS 上解压用的 unpacked)都要清掉。
func TestDownloadRemovesOtherVersions(t *testing.T) {
	dir := t.TempDir()
	for i := 3; i <= 16; i++ {
		touch(t, filepath.Join(dir, assetName("0.6."+strconv.Itoa(i))))
	}
	touch(t, filepath.Join(dir, assetName("0.7.16")))
	touch(t, filepath.Join(dir, assetName("0.7.17-rc.1")))
	if err := os.MkdirAll(filepath.Join(dir, "unpacked", "godusevpn.app"), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("new installer 0.7.17")
	srv, rel := signedServer(t, "0.7.17", payload)
	path, err := Download(context.Background(), rel, dir, srv.Client(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := listDir(t, dir), []string{assetName("0.7.17")}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("下载后目录里还有别的东西: %v", got)
	}
	if b, _ := os.ReadFile(path); string(b) != string(payload) {
		t.Fatalf("下好的安装包内容不对: %q", b)
	}
}

// 下到一半断了(或者被取消),半截文件不能留在目录里:它不会被装,下次也认不出来。
func TestDownloadRemovesPartialFileOnError(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	old := releasePublicKey
	releasePublicKey = pub
	t.Cleanup(func() { releasePublicKey = old })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		_, _ = w.Write([]byte("only the first few bytes"))
		// 处理函数返回时连接被掐断:客户端读到 unexpected EOF
	}))
	defer srv.Close()
	dir := t.TempDir()
	rel := &Release{Version: "0.7.17", InstallerURL: srv.URL + "/" + assetName("0.7.17"), SumsURL: srv.URL + "/SHA256SUMS", SumsSigURL: srv.URL + "/SHA256SUMS.sig"}
	if _, err := Download(context.Background(), rel, dir, srv.Client(), nil); err == nil {
		t.Fatal("下载被截断却没报错")
	}
	if got := listDir(t, dir); len(got) != 0 {
		t.Fatalf("下载失败后留下了半截文件: %v", got)
	}
}
