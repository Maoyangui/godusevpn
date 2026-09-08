// Package update 自更新:查 GitHub Releases,下载对应架构的安装包,校验 SHA256SUMS,交给调用方以管理员身份静默安装。
//
// 查版本先走 REST 接口;接口对未登录调用按出口 IP 限流(经代理时出口是共享 IP,常见 403 / 429),
// 不通就改读发布页的 Atom 订阅(普通网页,不受接口限流),下载地址按 tag 拼出来。
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// 测试时可改
var (
	releasesAPI  = "https://api.github.com/repos/Maoyangui/godusevpn/releases?per_page=10"
	releasesAtom = "https://github.com/Maoyangui/godusevpn/releases.atom"
	downloadBase = "https://github.com/Maoyangui/godusevpn/releases/download/"
)

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

// assetName 本平台对应的发布资产名。资产统一叫 godusevpn-<版本>-<系统>-<架构>.<后缀>,发布页按名字排序时同一系统的就挨在一起:
// Windows 是安装包(x64 / arm64),Android 是按 ABI 分的 APK,Linux 是 tar.gz(内含 godusevpn 二进制)。
func assetName(version string) string {
	arch := runtime.GOARCH
	switch runtime.GOOS {
	case "windows":
		if arch == "arm64" {
			return fmt.Sprintf("godusevpn-%s-windows-arm64-setup.exe", version)
		}
		return fmt.Sprintf("godusevpn-%s-windows-x64-setup.exe", version)
	case "android":
		switch arch {
		case "arm":
			arch = "armv7"
		case "amd64":
			arch = "x86_64"
		case "386":
			arch = "x86"
		}
		return fmt.Sprintf("godusevpn-%s-android-%s.apk", version, arch)
	}
	if arch == "arm" {
		arch = "armv7"
	}
	return fmt.Sprintf("godusevpn-%s-%s-%s.tar.gz", version, runtime.GOOS, arch)
}

// Check 有更新返回它,没有返回 nil。includePre 为真时预发布也算。
func Check(ctx context.Context, current string, includePre bool, client *http.Client) (*Release, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	rel, apiErr := checkAPI(ctx, current, includePre, client)
	if apiErr == nil {
		return rel, nil
	}
	rel, atomErr := checkAtom(ctx, current, includePre, client)
	if atomErr == nil {
		return rel, nil
	}
	return nil, fmt.Errorf("%v;备用通道也失败: %v", apiErr, atomErr)
}

func get(ctx context.Context, client *http.Client, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("User-Agent", "godusevpn-updater")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusForbidden, http.StatusTooManyRequests:
			return nil, fmt.Errorf("GitHub 接口限流(HTTP %d,经代理时出口 IP 是共享的)", resp.StatusCode)
		default:
			return nil, fmt.Errorf("GitHub 返回 HTTP %d", resp.StatusCode)
		}
	}
	return resp, nil
}

func checkAPI(ctx context.Context, current string, includePre bool, client *http.Client) (*Release, error) {
	resp, err := get(ctx, client, releasesAPI, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
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

var tagRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
var htmlTag = regexp.MustCompile(`<[^>]*>`)

// checkAtom 读发布页 Atom:条目按时间倒序,link 末段就是 tag。Atom 里没有"预发布"标记,按 tag 带不带 "-" 后缀判断。
// 下载地址按约定拼出来,再 HEAD 一下确认本机架构的安装包真的存在(顺便拿大小)。
func checkAtom(ctx context.Context, current string, includePre bool, client *http.Client) (*Release, error) {
	resp, err := get(ctx, client, releasesAtom, "application/atom+xml")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var feed struct {
		Entries []struct {
			Title string `xml:"title"`
			Link  struct {
				Href string `xml:"href,attr"`
			} `xml:"link"`
			Content string `xml:"content"`
		} `xml:"entry"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&feed); err != nil {
		return nil, fmt.Errorf("解析发布列表: %w", err)
	}
	for _, e := range feed.Entries {
		tag := e.Link.Href[strings.LastIndex(e.Link.Href, "/")+1:]
		if !tagRe.MatchString(tag) {
			continue
		}
		ver := strings.TrimPrefix(tag, "v")
		pre := strings.Contains(ver, "-")
		if (pre && !includePre) || !Newer(ver, current) {
			continue
		}
		rel := &Release{
			Version: ver, Tag: tag, Prerelease: pre,
			Notes:        strings.TrimSpace(html.UnescapeString(htmlTag.ReplaceAllString(e.Content, ""))),
			InstallerURL: downloadBase + tag + "/" + assetName(ver),
			SumsURL:      downloadBase + tag + "/SHA256SUMS",
		}
		if size, ok := head(ctx, client, rel.InstallerURL); !ok {
			continue // 没有本机架构的安装包,看下一个
		} else {
			rel.Size = size
		}
		return rel, nil
	}
	return nil, nil
}

func head(ctx context.Context, client *http.Client, url string) (int64, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("User-Agent", "godusevpn-updater")
	resp, err := client.Do(req)
	if err != nil {
		return 0, true // 网络抖动不算"没有",让下载阶段自己报错
	}
	resp.Body.Close()
	return resp.ContentLength, resp.StatusCode == http.StatusOK
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
