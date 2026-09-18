// Package ruleset 规则集(geosite / geoip 的 .srs)的落地与查找。
//
// 为什么要有这个包:内核在**启动阶段**就要把所有规则集读进来,读不到就整个起不来
// (sing-box 的 RemoteRuleSet 在没有缓存时是同步下载的,失败直接让 box.Start() 返回错误)。
// 早先客户端把规则集全写成 type: remote,从 GitHub 现下 —— 于是一台全新设备只要第一次下不动,
// 就永远连不上,界面只显示一句没有信息量的「内核启动失败」。2026-09-18 用户的索尼电视就是这样。
//
// 现在的做法:常用的三个规则集随安装包带上,启动一律用本地文件;别的规则集由守护进程自己去下,
// 下不到就把用到它的那条规则摘掉 —— **任何情况下都不让规则集拖垮内核启动**。
//
// 目录分三层,查找时按顺序取第一个命中的,各层只有一个写入者,互不覆盖:
//
//	<root>/<tag>.srs             用户自己放的,优先级最高,我们从不写这一层
//	<root>/downloaded/<tag>.srs  运行时补下来的,守护进程写
//	<root>/builtin/<tag>.srs     安装包自带的,每次启动按内置内容对齐
package ruleset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/common/srs"
)

// data 里的三个是"默认配置一定会用到"的:规则模式的国内直连靠 geosite-cn + geoip-cn,
// 广告拦截开关靠 geosite-category-ads-all。带上它们,默认设置下的连接就不需要联网拿规则集。
//
//go:embed data/*.srs
var builtinFS embed.FS

const (
	subDownloaded = "downloaded"
	subBuiltin    = "builtin"
)

// Builtin 内置规则集的标签,按名字排序。
func Builtin() []string {
	ents, err := builtinFS.ReadDir("data")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ents))
	for _, e := range ents {
		if name := strings.TrimSuffix(e.Name(), ".srs"); name != e.Name() {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Bytes 内置的那一份原始内容。没有这个标签时返回 nil。
func Bytes(tag string) []byte {
	b, err := builtinFS.ReadFile("data/" + tag + ".srs")
	if err != nil {
		return nil
	}
	return b
}

// Dirs 查找顺序:用户放的 → 下载来的 → 内置的。
func Dirs(root string) []string {
	if root == "" {
		return nil
	}
	return []string{root, filepath.Join(root, subDownloaded), filepath.Join(root, subBuiltin)}
}

// validTag 标签能不能安全地拼进路径。
//
// 标签最终来自用户在「路由规则」里填的 geosite / geoip 类别。settings 那边的 geoName 已经挡掉了
// 斜杠和反斜杠,所以今天穿不出目录 —— 但这个包会按标签**删文件、写文件**,而且是公开 API:
// 哪天有人从别的地方喂进来一个没规范化过的值,`filepath.Join(dir, "geosite-../../../x.srs")`
// 会规规矩矩地把路径清成目录外面去。Windows 上服务以 SYSTEM 跑,那就不只是掉几个文件的事。
// 门放在最贴近危险动作的地方,不指望上游永远记得校验。
func validTag(tag string) bool {
	if tag == "" || len(tag) > 96 || strings.Contains(tag, "..") {
		return false
	}
	for _, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// Find 按 Dirs 的顺序找一份**内核确实读得动**的 <tag>.srs。
//
// 为什么非要真的解一遍:本地规则集读不出来,和远程规则集下不到是一样的后果 —— 内核整个起不来。
// 一份被截断的文件(下载断了、掉电、磁盘坏块)会把客户端变成永远连不上,而且错误信息毫无指向性。
// 所以这里宁可当它不存在:我们自己那两层(下载的、内置的)顺手删掉,让下次启动重新铺 / 重新下;
// 用户自己放的那份不动,只是跳过去用下一层,并把原因记下来。
func Find(root, tag string) (string, bool) {
	if !validTag(tag) {
		return "", false
	}
	dirs := Dirs(root)
	for i, dir := range dirs {
		p := filepath.Join(dir, tag+".srs")
		st, err := os.Stat(p)
		if err != nil || st.IsDir() || st.Size() == 0 {
			continue
		}
		if err := checkFile(p, st); err != nil {
			note(p, err)
			if i > 0 { // 0 是用户自己的那一层,我们不动
				_ = os.Remove(p)
			}
			continue
		}
		return p, true
	}
	return "", false
}

// Broken 上一轮 Find 撞见的坏文件(路径 → 原因)。给日志和诊断包用。
func Broken() map[string]string {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	out := make(map[string]string, len(broken))
	for k, v := range broken {
		out[k] = v
	}
	return out
}

type stamp struct {
	size int64
	mod  int64
}

var (
	cacheMu sync.Mutex
	good    = map[string]stamp{}  // 解过一遍、没问题的文件:大小和时间没变就不再解
	broken  = map[string]string{} // 解不开的文件与原因
)

// checkFile 把文件整个解一遍。同一个文件不重复解:规则集一连接就要查一次,而内容几乎从不变。
func checkFile(path string, st os.FileInfo) error {
	key := stamp{size: st.Size(), mod: st.ModTime().UnixNano()}
	cacheMu.Lock()
	if s, ok := good[path]; ok && s == key {
		cacheMu.Unlock()
		return nil
	}
	cacheMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := Valid(data); err != nil {
		return err
	}
	cacheMu.Lock()
	good[path] = key
	delete(broken, path)
	cacheMu.Unlock()
	return nil
}

func note(path string, err error) {
	cacheMu.Lock()
	broken[path] = err.Error()
	delete(good, path)
	cacheMu.Unlock()
}

// Install 把内置规则集写进 <root>/builtin。内容一模一样就不动它,避免每次启动都白写一遍。
// 升级后内置内容变了会覆盖 —— 这一层本来就归我们管;用户自己放的在上一层,盖不着。
func Install(root string) error {
	if root == "" {
		return errors.New("规则集目录为空")
	}
	dir := filepath.Join(root, subBuiltin)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, tag := range Builtin() {
		want, err := builtinFS.ReadFile("data/" + tag + ".srs")
		if err != nil {
			return err
		}
		p := filepath.Join(dir, tag+".srs")
		if same(p, want) {
			continue
		}
		if err := writeFile(p, want); err != nil {
			return fmt.Errorf("写入内置规则集 %s: %w", tag, err)
		}
	}
	return nil
}

// Save 校验后写进 <root>/downloaded。校验不过不落盘:宁可这条规则暂时不生效,
// 也不能把一份坏文件留在那儿 —— 本地规则集读不出来同样会让内核起不来。
func Save(root, tag string, data []byte) error {
	if !validTag(tag) {
		return fmt.Errorf("规则集标签不合法: %q", tag)
	}
	if err := Valid(data); err != nil {
		return err
	}
	dir := filepath.Join(root, subDownloaded)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return writeFile(filepath.Join(dir, tag+".srs"), data)
}

// Valid 这份 .srs 内核读不读得动。整份解一遍,而不是只看开头三个字节:
// 下载被中途截断、或者拿回来的其实是一页 HTML 错误提示,都得在落盘之前拦住。
func Valid(data []byte) error {
	if len(data) == 0 {
		return errors.New("规则集是空的")
	}
	if _, err := srs.Read(bytes.NewReader(data), false); err != nil {
		return fmt.Errorf("规则集解不开(共 %d 字节): %w", len(data), err)
	}
	return nil
}

// Fetch 下载一个规则集并存到 <root>/downloaded。client 为 nil 时用带超时的默认客户端。
func Fetch(ctx context.Context, client *http.Client, root, tag, url string) error {
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	// 规则集就几十到几百 KB,给 8MB 的上限,免得地址被劫持成一个无底洞把内存吃光
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	return Save(root, tag, data)
}

func same(path string, want []byte) bool {
	got, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return sha256.Sum256(got) == sha256.Sum256(want)
}

// writeFile 先写临时文件再改名:写到一半断电也不会留下一个半截的规则集,
// 那种文件下次启动会让内核起不来,正是这个包要根治的毛病。
func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
