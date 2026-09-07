// Package update 自更新:查 GitHub Releases,下载对应架构的安装包,校验 SHA256SUMS,交给调用方以管理员身份静默安装。
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const releasesAPI = "https://api.github.com/repos/Maoyangui/godusevpn/releases?per_page=10"

type Release struct {
	Version      string `json:"version"`
	Tag          string `json:"tag"`
	Notes        string `json:"notes"`
	Prerelease   bool   `json:"prerelease"`
	InstallerURL string `json:"installerUrl"`
	SumsURL      string `json:"sumsUrl"`
	Size         int64  `json:"size"`
}

// parse "1.2.3" / "1.2.3-m1" → 数字三段 + 是否带后缀
func parse(v string) ([3]int, bool, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	main, suffix, hasSuffix := strings.Cut(v, "-")
	_ = suffix
	parts := strings.Split(main, ".")
	var out [3]int
	if len(parts) != 3 {
		return out, false, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false, false
		}
		out[i] = n
	}
	return out, hasSuffix, true
}

// Newer a 是否比 b 新:数字大的新;数字相同时不带后缀的(正式版)比带后缀的新。
func Newer(a, b string) bool {
	x, xs, okx := parse(a)
	y, ys, oky := parse(b)
	if !okx || !oky {
		return false
	}
	for i := 0; i < 3; i++ {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return ys && !xs
}

func assetName(version string) string {
	arch := "x64"
	if runtime.GOARCH == "arm64" {
		arch = "arm64"
	}
	return fmt.Sprintf("godusevpn-%s-%s-setup.exe", version, arch)
}

// Check 有更新返回它,没有返回 nil。includePre 为真时预发布也算。
func Check(ctx context.Context, current string, includePre bool, client *http.Client) (*Release, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "godusevpn-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub 返回 HTTP %d", resp.StatusCode)
	}
	var list []struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Body       string `json:"body"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	for _, r := range list {
		if r.Draft || (r.Prerelease && !includePre) {
			continue
		}
		ver := strings.TrimPrefix(r.TagName, "v")
		if !Newer(ver, current) {
			continue
		}
		rel := &Release{Version: ver, Tag: r.TagName, Notes: r.Body, Prerelease: r.Prerelease}
		want := assetName(ver)
		for _, a := range r.Assets {
			switch a.Name {
			case want:
				rel.InstallerURL, rel.Size = a.URL, a.Size
			case "SHA256SUMS":
				rel.SumsURL = a.URL
			}
		}
		if rel.InstallerURL == "" {
			continue // 这个版本没有本机架构的安装包,看下一个
		}
		return rel, nil
	}
	return nil, nil
}

// Download 下载安装包到 dir,按 SHA256SUMS 校验;progress 每读一块回调一次。
func Download(ctx context.Context, rel *Release, dir string, client *http.Client, progress func(done, total int64)) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, assetName(rel.Version))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.InstallerURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "godusevpn-updater")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载失败: HTTP %d", resp.StatusCode)
	}
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	var done int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return "", werr
			}
			h.Write(buf[:n])
			done += int64(n)
			if progress != nil {
				progress(done, resp.ContentLength)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return "", rerr
		}
	}
	f.Close()
	if rel.SumsURL != "" {
		want, err := fetchSum(ctx, client, rel.SumsURL, filepath.Base(path))
		if err != nil {
			return "", fmt.Errorf("读取校验和: %w", err)
		}
		if got := hex.EncodeToString(h.Sum(nil)); want != "" && got != want {
			os.Remove(path)
			return "", errors.New("安装包校验失败,已删除")
		}
	}
	return path, nil
}

func fetchSum(ctx context.Context, client *http.Client, url, name string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "godusevpn-updater")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", nil
}
