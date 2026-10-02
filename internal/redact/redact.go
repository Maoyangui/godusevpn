// Package redact 订阅地址与凭据的统一打码。写进日志的每一行(logx)、拉订阅失败的错误文本(profile)、
// 命令行 diag 与诊断包(daemon)都走这里,规则只有这一份。
//
// 打码后要看得出原来是什么:地址留协议与主机,userinfo 换成 ***@,路径 / 查询 / 片段整段换成 /***;
// 凭据留键名、值换成 ***;订阅令牌出现在别处时换成 <订阅令牌>;MAC 留厂商段。空值照旧留空。
//
// 只处理给人看的那一份,从不回写设置与订阅缓存(导出诊断包曾经把内存里的订阅链接改坏过)。
package redact

import (
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

var (
	// 文本里的地址。全角标点与中文标点不算地址的一部分(日志正文是中文,地址后面常常紧跟着"，")。
	textURL = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s<>"'\x60\x{3000}-\x{303F}\x{FF00}-\x{FFEF}]+`)
	subPath = regexp.MustCompile(`(?i)(/sub/)[^\s/?#"'<>]+`)
	bearer  = regexp.MustCompile(`(?i)\bBearer[ \t]+[^\s,"'<>]+`)
	// key: value / key=value 形式的凭据。裸值不许以 { [ 开头:诊断包里整段 JSON 也过这一遍,
	// "obfs": { 这样的对象要是被当成值吃掉,文件就不再是合法 JSON。
	credential = regexp.MustCompile(`(?i)\b([a-z_-]*(?:password|passwd|passphrase|token|secret)|private[_-]?key|pre[_-]?shared[_-]?key|client[_-]?key|user[_-]?key|api[_-]?key|authorization|auth[_-]?str|uuid|psk|username|obfs)("?[ \t]*[:=][ \t]*)("[^"\r\n]*"|'[^'\r\n]*'|[^\s,;"'{}\[\]]+)`)
	// 主机位要像个主机名(带点的域名或 localhost)或 IP。vmess://、老格式 ss:// 把整段凭据 base64 进主机位,那样的整段打码。
	hostLike = regexp.MustCompile(`^(?:[A-Za-z0-9_-]+\.)+[A-Za-z0-9-]+$|^localhost$`)
	// MAC(含 DHCPv6 DUID 这种更长的串):- 与 : 两种写法。
	macAddr = regexp.MustCompile(`[0-9A-Fa-f]{2}(?:-[0-9A-Fa-f]{2}){5,}|[0-9A-Fa-f]{2}(?::[0-9A-Fa-f]{2}){5,}`)
)

// URL 地址只留协议与主机。
func URL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return "***"
	}
	scheme := strings.ToLower(u.Scheme)
	if h := u.Hostname(); h == "" || (net.ParseIP(h) == nil && !hostLike.MatchString(h)) {
		return scheme + "://***"
	}
	out := scheme + "://"
	if u.User != nil {
		out += "***@"
	}
	out += u.Host
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return out + "/***"
	}
	return out + u.Path
}

// Text 自由文本(日志行、错误信息、诊断文件):地址按 URL 打码,Bearer、key=value 形式的凭据、/sub/ 后面的令牌打码。
//
// 日志的每一行都过这里(内核日志在 info 级别下一条连接一行),所以每条正则前先用子串预筛:
// 四条正则全跑一遍一行要几十微秒,软路由上要再慢一个数量级;预筛不中的行(绝大多数)只花一次 ToLower。
func Text(s string) string {
	// 日志里可能有 JSON 转义过的地址(https:\/\/…)
	s = strings.ReplaceAll(s, `\/`, `/`)
	low := strings.ToLower(s) // 只用来预筛:前面的替换只会删字,不会凭空造出关键词,不必重算
	if strings.Contains(s, "://") {
		s = textURL.ReplaceAllStringFunc(s, urlInText)
	}
	if strings.Contains(low, "bearer") {
		s = bearer.ReplaceAllString(s, "Bearer ***")
	}
	if containsAny(low, credentialHints) {
		s = credential.ReplaceAllStringFunc(s, maskCredential)
	}
	if strings.Contains(low, "/sub/") {
		s = subPath.ReplaceAllString(s, "${1}***")
	}
	return s
}

// credentialHints credential 正则里每个键名都含其中之一(小写)。改正则时这里要跟着改,TestCredentialHintsCoverKeys 钉着。
var credentialHints = []string{"pass", "token", "secret", "key", "auth", "uuid", "psk", "username", "obfs"}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// urlInText 句末标点与没配对的右括号不算地址,留在原处。
func urlInText(m string) string {
	end := len(m)
	for end > 0 {
		c := m[end-1]
		if strings.IndexByte(".,;:!?", c) >= 0 ||
			(c == ')' && strings.Count(m[:end], "(") < strings.Count(m[:end], ")")) ||
			(c == ']' && strings.Count(m[:end], "[") < strings.Count(m[:end], "]")) {
			end--
			continue
		}
		break
	}
	return URL(m[:end]) + m[end:]
}

// maskCredential 值换成 ***,引号原样保留:诊断包里的 JSON 是整段过这个正则的,吃掉闭合引号文件就坏了。
func maskCredential(m string) string {
	sub := credential.FindStringSubmatch(m)
	if len(sub) < 4 {
		return m
	}
	key, sep, val := sub[1], sub[2], sub[3]
	if val == "***" || strings.EqualFold(val, "Bearer") {
		return m // 已经打过码(Authorization: Bearer *** 留着 Bearer,看得出原来是什么)
	}
	if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
		if len(val) == 2 {
			return m // 空值照旧:看得出本来就没有
		}
		return key + sep + string(val[0]) + "***" + string(val[0])
	}
	return key + sep + "***"
}

// SecretKey JSON 里这个键的值是不是凭据(大小写、_ - . 不计)。
func SecretKey(key string) bool {
	k := norm(key)
	switch k {
	case "uuid", "psk", "auth", "authstr", "authorization", "proxyauthorization", "credential", "credentials", "cookie", "user", "username":
		return true
	case "publickey":
		return false
	}
	for _, suf := range []string{"password", "passwd", "passphrase", "token", "secret", "key"} {
		if strings.HasSuffix(k, suf) {
			return true // key 结尾:private_key、pre_shared_key、client_key、userkey、static_key……public_key 除外
		}
	}
	return false
}

func norm(key string) string {
	return strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(key))
}

// Masker 一次诊断输出(命令行 diag、诊断包)用的打码器。在 Text 之外还做两件事:
// 订阅令牌出现在别处时盖掉 —— m-ui 默认拿用户名当订阅路径,订阅名、socks 用户名又常常就是它,
// 只盖地址的话"主机 + /sub/ + 名字"一拼就还原了;MAC 只留厂商段。
type Masker struct {
	tokens []*regexp.Regexp
}

// NewMasker 从这台设备上的订阅地址里取令牌:userinfo、路径最后一段(路径至少两段时)、较长的查询参数值。
func NewMasker(subURLs ...string) *Masker {
	seen := map[string]bool{}
	var toks []string
	add := func(t string, min int) {
		if len(t) >= min && !seen[t] {
			seen[t] = true
			toks = append(toks, t)
		}
	}
	for _, raw := range subURLs {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		if u.User != nil {
			add(u.User.Username(), 3)
			if p, ok := u.User.Password(); ok {
				add(p, 3)
			}
		}
		inQuery := false
		for _, vs := range u.Query() {
			for _, v := range vs {
				if len(v) >= 8 { // format=json 这类短值不是令牌
					add(v, 8)
					inQuery = true
				}
			}
		}
		// 令牌在查询参数里时(/api/v1/client/subscribe?token=…),路径最后一段只是接口名
		if segs := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' }); len(segs) >= 2 && !inQuery {
			add(segs[len(segs)-1], 3)
		}
	}
	sort.Slice(toks, func(i, j int) bool { return len(toks[i]) > len(toks[j]) }) // 长的先换,免得留下半截
	m := &Masker{}
	for _, t := range toks {
		m.tokens = append(m.tokens, regexp.MustCompile(regexp.QuoteMeta(t)))
	}
	return m
}

// Text 通用规则 + 订阅令牌 + MAC。
func (m *Masker) Text(s string) string {
	s = Text(s)
	for _, re := range m.tokens {
		s = replaceBounded(s, re, wordEdge, func(string) string { return "<订阅令牌>" })
	}
	return replaceBounded(s, macAddr, macEdge, maskMAC)
}

// Tree 打码一棵 JSON 树(map[string]any / []any / 标量),返回新树,不改入参。
// 凭据字段整值换成 ***;以 url 结尾的字段按地址处理;hysteria v1 的 obfs 是字符串形式的混淆密码,
// hysteria2 的 obfs 是对象(里面的 password 照常打码),所以只在值是字符串时整值打码。
func (m *Masker) Tree(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			s, isStr := val.(string)
			switch {
			case val == nil, isStr && s == "":
				out[k] = val
			case SecretKey(k) || (norm(k) == "obfs" && isStr):
				if _, isBool := val.(bool); isBool {
					out[k] = val
				} else {
					out[k] = "***"
				}
			case isStr && strings.HasSuffix(strings.ToLower(k), "url"):
				out[k] = m.Text(URL(s))
			default:
				out[k] = m.Tree(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = m.Tree(x[i])
		}
		return out
	case string:
		return m.Text(x)
	default:
		return v
	}
}

func maskMAC(s string) string {
	hex := strings.Map(func(r rune) rune {
		if r == ':' || r == '-' || r == ' ' {
			return -1
		}
		return r
	}, s)
	if strings.Trim(hex, "0") == "" || strings.Trim(strings.ToLower(hex), "f") == "" {
		return s // 全 0 / 广播地址不是谁的身份
	}
	sep := s[2:3]
	parts := strings.Split(s, sep)
	for i := 3; i < len(parts); i++ {
		parts[i] = "xx"
	}
	return strings.Join(parts, sep)
}

func isAlnum(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// wordEdge 令牌 / 主机名前后紧挨着字母数字就不算(免得把长词里的一截当成它)。
func wordEdge(s string, i, j int) bool {
	return (i == 0 || !isAlnum(s[i-1])) && (j == len(s) || !isAlnum(s[j]))
}

// macEdge MAC 前后不能再连着十六进制或分隔符:IPv6 地址里恰好是六组两位的那一截不算。
func macEdge(s string, i, j int) bool {
	hexOrSep := func(c byte) bool { return isAlnum(c) || c == ':' || c == '-' || c == '.' }
	return (i == 0 || !hexOrSep(s[i-1])) && (j == len(s) || !hexOrSep(s[j]))
}

// replaceBounded 把 re 的每个匹配交给 fn;edge 说前后不合适的跳过。
func replaceBounded(s string, re *regexp.Regexp, edge func(s string, i, j int) bool, fn func(string) string) string {
	var b strings.Builder
	last := 0
	for _, m := range re.FindAllStringIndex(s, -1) {
		if !edge(s, m[0], m[1]) {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(fn(s[m[0]:m[1]]))
		last = m[1]
	}
	if last == 0 && b.Len() == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}
