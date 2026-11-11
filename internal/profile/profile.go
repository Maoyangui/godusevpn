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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/redact"
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
	URL   string `json:"url"`
	Title string `json:"title"`
	// WebPage 面板给的「选购 / 续费」地址(Profile-Web-Page-Url 头),空 = 面板没配。
	// 面板那边已经按"代理填了用代理的,否则用主面板的"解析过,这里拿到的就是这条订阅该去的地方。
	WebPage   string            `json:"webPage,omitempty"`
	FetchedAt int64             `json:"fetchedAt"`
	Outbounds []json.RawMessage `json:"outbounds"` // 清洗后的节点出站
	Tags      []string          `json:"tags"`
	Usage     Usage             `json:"usage"`
	Manual    bool              `json:"manual,omitempty"` // 手动添加的节点,不用刷新
	// Groups 订阅里的线路组(见 Group),按订阅里的顺序。旧版本存的缓存没有这一项,刷新一次就有了。
	Groups []Group `json:"groups,omitempty"`
	// Whole 成员正好是全部节点、所以没收进 Groups 的线路组的名字(见 parseGroups):它和自动选择是一回事,界面上不单列。
	// 只用来认出"选着的线路组还在,只是订阅里只剩这一条线路了"(面板停了别的线路、把别的线路从这个用户名下拿掉):
	// 这时这一轮就用自动选择(它就是这条线路,见 builder.Effective),而不是当成线路减到一台、把人固定在一台服务器上;
	// 之后这条线路再减到一台、又加回别的线路时,也还知道它原来是哪几台(就是这份订阅的全部节点)。
	Whole []string `json:"whole,omitempty"`
	// Parser 这份缓存是哪一版解析器读出来的。比 parserVersion 旧的(包括没有这一项的)算陈旧(Outdated):
	// 升级后连上就经隧道补拉一次,新版本多认出来的东西(线路组、Whole)不用等满刷新间隔。
	Parser int `json:"parser,omitempty"`
}

// Group 订阅里的线路组:sing-box 的 urltest 出站,成员都是本订阅里的节点。m-ui 给部署在多台服务器上的线路出这种组
// (同一条线路、不同的入口服务器),客户端选它,就在这几台里挑最快的,线路和出口不变。
// 只记成员:订阅给的测速地址和间隔一概不用 —— 不然第三方订阅能让每个节点定时去访问它指定的地址。
type Group struct {
	Tag     string   `json:"tag"`
	Members []string `json:"members"`
}

// parserVersion 见 Profile.Parser。1 = 认线路组;2 = 也记下成员是全部节点的线路组(Profile.Whole)。
const parserVersion = 2

// 这些 tag 是客户端自己用的,节点不能撞;这些类型不是节点,直接丢(urltest 另外收成线路组,见 Parse)
var reservedTags = map[string]bool{"proxy": true, "auto": true, "direct": true, "block": true, "dns-out": true}
var nonNodeTypes = map[string]bool{"selector": true, "urltest": true, "direct": true, "block": true, "dns": true}

// dropKeys 节点出站里客户端一概不要的字段:节点域名怎么解析由客户端自己定(dns.strategy 加 route.default_domain_resolver,见 builder)。
//   - domain_resolver:第三方订阅常让它指向订阅自己的 DNS 服务器,而订阅的 DNS 段客户端整个不要,留着这个节点就建不起来。
//   - domain_strategy:sing-box 已废弃的写法,Sub-Store 之类会把 Clash 的 ip-version 转成它,旧模板也常写 prefer_ipv4。
//     出站写了它、又没写 domain_resolver 时,sing-box 拿它盖掉解析策略(common/dialer 的 NewWithOptions),嵌入方式下没有
//     deprecated 的管理器,既不报错也不拦:关着 IPv6 时 dns.strategy 是 ipv4_only,写了 prefer_ipv6 / ipv6_only 的节点
//     照样解析 AAAA、经 IPv6 去连,prefer_ipv4 的在 IPv4 慢或不通时也会并行回落到 IPv6 —— 关着 IPv6 就不走 IPv6 落空。
var dropKeys = []string{"domain_resolver", "domain_strategy"}

// cleanNode 去掉节点出站里的 dropKeys;tag 不为空、又和出站里写的不一样时改成它。什么都不用改就原样返回(字节不变),
// 不是 JSON 对象返回 false。只动顶层这几个键,别的字段原样保留。
func cleanNode(raw json.RawMessage, tag string) (json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return raw, false
	}
	changed := false
	for _, k := range dropKeys {
		if _, ok := m[k]; ok {
			delete(m, k)
			changed = true
		}
	}
	if tag != "" {
		var cur string
		if json.Unmarshal(m["tag"], &cur) != nil || cur != tag {
			m["tag"], _ = json.Marshal(tag)
			changed = true
		}
	}
	if !changed {
		return raw, true
	}
	b, err := json.Marshal(m)
	if err != nil {
		return raw, false
	}
	return b, true
}

// Parse 解析订阅正文与响应头。
func Parse(body []byte, hdr http.Header) (*Profile, error) {
	var doc struct {
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("订阅不是 sing-box JSON: %w", err)
	}
	p := &Profile{FetchedAt: time.Now().Unix(), Parser: parserVersion}
	used := map[string]bool{}
	for k := range reservedTags {
		used[k] = true
	}
	renamed := map[string]string{} // 订阅里节点原来的 tag → 清洗后的 tag(线路组的成员按原 tag 写)
	var urltests []json.RawMessage
	for _, raw := range doc.Outbounds {
		var meta struct {
			Type string `json:"type"`
			Tag  string `json:"tag"`
		}
		if json.Unmarshal(raw, &meta) != nil || meta.Type == "" {
			continue
		}
		if meta.Type == "urltest" {
			urltests = append(urltests, raw)
			continue
		}
		if nonNodeTypes[meta.Type] {
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
		// 改过名字要写回出站;节点自带的解析设置一并去掉(见 dropKeys)
		var ok bool
		if raw, ok = cleanNode(raw, tag); !ok {
			continue
		}
		if _, dup := renamed[meta.Tag]; !dup && meta.Tag != "" {
			renamed[meta.Tag] = tag
		}
		p.Outbounds = append(p.Outbounds, raw)
		p.Tags = append(p.Tags, tag)
	}
	if len(p.Outbounds) == 0 {
		return nil, ErrNoNodes
	}
	p.Groups, p.Whole = parseGroups(urltests, renamed, len(p.Tags), used)
	if hdr != nil {
		p.Title = decodeTitle(hdr.Get("Profile-Title"))
		p.Usage = parseUserinfo(hdr.Get("Subscription-Userinfo"))
		p.WebPage = safeWebPage(hdr.Get("Profile-Web-Page-Url"))
	}
	return p, nil
}

// parseGroups 从订阅的 urltest 出站里收线路组。outbounds 那一项单独解,不和节点共用一份 meta:
// 第三方出站里要是有个同名、类型却不对的字段,不能连累节点被丢掉。
//
//   - 成员按 renamed 换成清洗后的节点名;对不上的(direct、别的组、不存在的)丢掉,组内去重;
//   - 剩不到 2 个成员的不要;tag 是客户端保留名的不要;成员集合和前面某个组一模一样的不要;
//   - 成员正好是全部节点的不收进线路组(那就是全局自动:别家的"自动选择",或者 m-ui 的订阅里只剩这一条线路),
//     只把名字记进 whole(见 Profile.Whole)。它们等正常的线路组都起完名字再起,不影响那些组叫什么;
//   - 组名去掉首尾空白,和节点、保留名、前面的组撞了加序号,保证全局唯一(used 会被改)。
func parseGroups(urltests []json.RawMessage, renamed map[string]string, nodes int, used map[string]bool) (out []Group, whole []string) {
	seen := map[string]bool{} // 成员集合(排序后拼起来)
	var wholeTags []string
	name := func(base string) string {
		tag := base
		for i := 2; used[tag]; i++ {
			tag = fmt.Sprintf("%s %d", base, i)
		}
		used[tag] = true
		return tag
	}
	for _, raw := range urltests {
		var g struct {
			Tag       string   `json:"tag"`
			Outbounds []string `json:"outbounds"`
		}
		if json.Unmarshal(raw, &g) != nil {
			continue
		}
		tag := strings.TrimSpace(g.Tag)
		if tag == "" || reservedTags[tag] {
			continue
		}
		var members []string
		in := map[string]bool{}
		for _, o := range g.Outbounds {
			if t, ok := renamed[o]; ok && !in[t] {
				in[t] = true
				members = append(members, t)
			}
		}
		if len(members) < 2 {
			continue
		}
		if len(members) == nodes {
			wholeTags = append(wholeTags, tag)
			continue
		}
		key := slices.Clone(members)
		slices.Sort(key)
		if k := strings.Join(key, "\x00"); seen[k] {
			continue
		} else {
			seen[k] = true
		}
		out = append(out, Group{Tag: name(tag), Members: members})
	}
	for _, tag := range wholeTags {
		whole = append(whole, name(tag))
	}
	return out, whole
}

// Group 按 tag 找线路组。
func (p *Profile) Group(tag string) (Group, bool) {
	if p == nil || tag == "" {
		return Group{}, false
	}
	for _, g := range p.Groups {
		if g.Tag == tag {
			return g, true
		}
	}
	return Group{}, false
}

// WholeGroup 按 tag 找成员是全部节点的线路组(见 Profile.Whole):成员就是这份订阅的全部节点。Group 找不到它。
// 节点里有同名的不算(选的是那个节点)。
func (p *Profile) WholeGroup(tag string) (Group, bool) {
	if p == nil || tag == "" || !slices.Contains(p.Whole, tag) || slices.Contains(p.Tags, tag) {
		return Group{}, false
	}
	return Group{Tag: tag, Members: slices.Clone(p.Tags)}, true
}

// GroupOf 第一个包含这个节点的线路组的 tag;不在任何组里返回空串。
func (p *Profile) GroupOf(node string) string {
	if p == nil || node == "" {
		return ""
	}
	for _, g := range p.Groups {
		if slices.Contains(g.Members, node) {
			return g.Tag
		}
	}
	return ""
}

// decodeTitle Profile-Title 可能是 base64:xxx(Clash 系约定)或 RFC 8187 的 utf-8”%E4%B8%AD 形式。
// safeWebPage 只认 http / https,别的一律当没有。
// 订阅是外部输入,不能让它塞一个 file: / javascript: 之类的东西进来再交给系统去打开。
func safeWebPage(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > 2048 {
		return ""
	}
	l := strings.ToLower(v)
	if !strings.HasPrefix(l, "http://") && !strings.HasPrefix(l, "https://") {
		return ""
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return v
}

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
	Status  int
	Msg     string
	WebPage string // 404 时面板随响应给的「选购 / 续费」地址:到期 / 用尽的人最需要它
}

func (e *FetchError) Error() string { return e.Msg }

// 到期 / 用尽的人拉订阅只会得到 404,面板会把「选购 / 续费」地址随这个 404 一起发。
// 错误从服务到界面一路都是字符串(IPC → 桌面壳 / 安卓桥 → 网页),所以地址就挂在错误文本末尾,
// 界面按同一个格式拆出来画成按钮;写进"最近一次失败原因"之前也先拆掉。
const renewMark = " [renew="

// WithRenew 把续费地址挂到错误文本末尾;没有地址就原样返回。
func WithRenew(msg, link string) string {
	if link == "" {
		return msg
	}
	return msg + renewMark + link + "]"
}

// SplitRenew 拆出 WithRenew 挂上去的地址;没挂就原样返回、地址为空。
func SplitRenew(msg string) (text, link string) {
	i := strings.LastIndex(msg, renewMark)
	if i < 0 || !strings.HasSuffix(msg, "]") {
		return msg, ""
	}
	return msg[:i], msg[i+len(renewMark) : len(msg)-1]
}

// jsonFormat 订阅地址里的 format 是不是 sing-box JSON 一族(m-ui 认的写法,不分大小写)。
func jsonFormat(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "json", "sing-box", "singbox", "sfa", "sfi", "sfm", "sft", "sfw", "sfl":
		return true
	}
	return false
}

// Fetch 拉订阅。客户端只认 sing-box JSON:地址不带 format、或带的是别的格式(用户从落地页复制了
// Clash 地址 ?format=clash、浏览器地址 ?format=page 等)都改成 format=json,粘哪个地址都能用。
func Fetch(ctx context.Context, rawURL string, client *http.Client) (*Profile, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, &FetchError{Msg: "订阅地址无效"}
	}
	q := u.Query()
	if !jsonFormat(q.Get("format")) {
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
		// 网络错误是 *url.Error,文本带着完整请求地址(Get "https://面板/sub/<令牌>?format=json": …),
		// 而这段文本会进界面、服务日志和诊断信息。地址只留协议与主机。
		return nil, &FetchError{Msg: "拉取订阅失败: " + redact.Text(err.Error())}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, &FetchError{Msg: "读取订阅失败: " + redact.Text(err.Error())}
	}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		// 到期 / 用尽的人拉到的就是这个 404,面板会把「选购 / 续费」地址随它一起发 —— 正是这时候最需要续费入口
		link := safeWebPage(resp.Header.Get("Profile-Web-Page-Url"))
		return nil, &FetchError{Status: resp.StatusCode, WebPage: link,
			Msg: WithRenew(fmt.Sprintf("面板不认识这条订阅链接,或账号已停用 / 用完 / 到期(HTTP %d,%s)", resp.StatusCode, u.Host), link)}
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
	// 旧版本存的缓存里节点可能还带着 domain_strategy(那时只去 domain_resolver):读进来就去掉,不用等下一次刷新(见 dropKeys)。
	// 只改内存里这份,和 Tags 一一对应,一条都不丢
	for i, raw := range p.Outbounds {
		if c, ok := cleanNode(raw, ""); ok {
			p.Outbounds[i] = c
		}
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
	if p != nil && p.Manual {
		return false
	}
	return p == nil || time.Since(time.Unix(p.FetchedAt, 0)) > every
}

// Outdated 旧版本的解析器读出来的缓存(见 Profile.Parser):节点照样能用,只是少了新版本才认得出来的东西(线路组、成员是全部节点的线路组),
// 值得补拉一次。不算过期 —— 不能为它在连上之前先直连拉一次订阅、挡着第一次连接;连上之后经隧道补拉(见 daemon.maybeRefresh)。
// 手动节点没有地方可拉,永远不算。
func (p *Profile) Outdated() bool {
	return p != nil && !p.Manual && p.Parser < parserVersion
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
