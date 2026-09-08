// Package profile 订阅:从面板拉 sing-box JSON 订阅,只取节点出站,其余(入站、DNS、路由)一律丢掉,
// 由客户端自己的策略重新生成。用量与到期来自 Subscription-Userinfo 头,名称来自 Profile-Title 头。
package profile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
)

// ErrNoNodes 订阅里一个节点都没有(面板给的是只有 direct 的空配置)。
var ErrNoNodes = errors.New("订阅里没有任何节点")

type Usage struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
	Total    int64 `json:"total"`  // 0 = 不限
	Expire   int64 `json:"expire"` // 0 = 不限
}

type Profile struct {
	URL       string            `json:"url"`
	Title     string            `json:"title"`
	FetchedAt int64             `json:"fetchedAt"`
	Outbounds []json.RawMessage `json:"outbounds"` // 清洗后的节点出站
	Tags      []string          `json:"tags"`
	Usage     Usage             `json:"usage"`
}

// 这些 tag 是客户端自己用的,节点不能撞;这些类型不是节点,直接丢
var reservedTags = map[string]bool{"proxy": true, "auto": true, "direct": true, "block": true, "dns-out": true}
var nonNodeTypes = map[string]bool{"selector": true, "urltest": true, "direct": true, "block": true, "dns": true}

// Parse 解析订阅正文与响应头。
func Parse(body []byte, hdr http.Header) (*Profile, error) {
	var doc struct {
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("订阅不是 sing-box JSON: %w", err)
	}
	p := &Profile{FetchedAt: time.Now().Unix()}
	used := map[string]bool{}
	for k := range reservedTags {
		used[k] = true
	}
	for _, raw := range doc.Outbounds {
		var meta struct {
			Type string `json:"type"`
			Tag  string `json:"tag"`
		}
		if json.Unmarshal(raw, &meta) != nil || meta.Type == "" || nonNodeTypes[meta.Type] {
			continue
		}
		tag := strings.TrimSpace(meta.Tag)
		if tag == "" {
			tag = meta.Type
		}
		base := tag
		for i := 2; used[tag]; i++ {
			tag = fmt.Sprintf("%s %d", base, i)
		}
		used[tag] = true
		if tag != meta.Tag { // 改过名字要写回出站
			var m map[string]any
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			m["tag"] = tag
			raw, _ = json.Marshal(m)
		}
		p.Outbounds = append(p.Outbounds, raw)
		p.Tags = append(p.Tags, tag)
	}
	if len(p.Outbounds) == 0 {
		return nil, ErrNoNodes
	}
	if hdr != nil {
		p.Title = decodeTitle(hdr.Get("Profile-Title"))
		p.Usage = parseUserinfo(hdr.Get("Subscription-Userinfo"))
	}
	return p, nil
}

// decodeTitle Profile-Title 可能是 base64:xxx(Clash 系约定)或 RFC 8187 的 utf-8”%E4%B8%AD 形式。
func decodeTitle(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(v), "base64:") {
		if b, err := base64.StdEncoding.DecodeString(v[7:]); err == nil {
			return strings.TrimSpace(string(b))
		}
		return ""
	}
	if i := strings.Index(v, "''"); i >= 0 {
		if s, err := url.PathUnescape(v[i+2:]); err == nil {
			return strings.TrimSpace(s)
		}
	}
	return v
}

// parseUserinfo "upload=1; download=2; total=3; expire=4"
func parseUserinfo(v string) Usage {
	var u Usage
	for _, part := range strings.Split(v, ";") {
		k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "upload":
			u.Upload = n
		case "download":
			u.Download = n
		case "total":
			u.Total = n
		case "expire":
			u.Expire = n
		}
	}
	return u
}

// FetchError 带 HTTP 状态码的拉取失败:404 / 410 是"订阅无效或已到期",和网络故障要分开提示。
type FetchError struct {
	Status int
	Msg    string
}

func (e *FetchError) Error() string { return e.Msg }

// Fetch 拉订阅。地址不带 format 参数时补上 format=json。
func Fetch(ctx context.Context, rawURL string, client *http.Client) (*Profile, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, &FetchError{Msg: "订阅地址无效"}
	}
	q := u.Query()
	if q.Get("format") == "" {
		q.Set("format", "json")
		u.RawQuery = q.Encode()
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", buildinfo.UserAgent())
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, &FetchError{Msg: "拉取订阅失败: " + err.Error()}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, &FetchError{Msg: "读取订阅失败: " + err.Error()}
	}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return nil, &FetchError{Status: resp.StatusCode, Msg: fmt.Sprintf("面板不认识这条订阅链接,或账号已停用 / 用完 / 到期(HTTP %d,%s)", resp.StatusCode, u.Host)}
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, &FetchError{Status: resp.StatusCode, Msg: "请求太频繁,稍后再试"}
	case resp.StatusCode != http.StatusOK:
		return nil, &FetchError{Status: resp.StatusCode, Msg: fmt.Sprintf("订阅服务器返回 HTTP %d", resp.StatusCode)}
	}
	p, err := Parse(body, resp.Header)
	if err != nil {
		return nil, err
	}
	p.URL = rawURL
	return p, nil
}

// Load / Save 订阅缓存(服务重启、拉取失败时用上一份)。
func Load(path string) (*Profile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	if len(p.Outbounds) == 0 {
		return nil, ErrNoNodes
	}
	return &p, nil
}

func (p *Profile) Save(path string) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Stale 距上次拉取是否超过 every。
func (p *Profile) Stale(every time.Duration) bool {
	return p == nil || time.Since(time.Unix(p.FetchedAt, 0)) > every
}

// Servers 节点的服务器地址(诊断包里看连通性用,不含凭据)。
func (p *Profile) Servers() []string {
	var out []string
	for _, raw := range p.Outbounds {
		var m struct {
			Server string `json:"server"`
			Port   int    `json:"server_port"`
		}
		if json.Unmarshal(raw, &m) == nil && m.Server != "" {
			out = append(out, fmt.Sprintf("%s:%d", m.Server, m.Port))
		}
	}
	return out
}
