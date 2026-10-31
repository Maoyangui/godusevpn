// Package nodelink 手动添加节点:分享链接转成 sing-box 出站,检查手填的节点。
//
// 错误文案绝不带链接原文:里面有密码,错误会进界面和日志。所以这里不把 url.Parse 的错误往外传(它会带上整条链接)。
package nodelink

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Error 一条链接或一个节点的问题。Code 给界面翻译,Error() 是中文(命令行和服务日志用)。
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func fail(code, msg string) error { return &Error{Code: code, Msg: msg} }

// CodeOf 取错误代码,不是本包的错误返回空。
func CodeOf(err error) string {
	if e, ok := err.(*Error); ok {
		return e.Code
	}
	return ""
}

// LineError 粘贴的第几行有问题。
type LineError struct {
	Line int    `json:"line"`
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

// ParseAll 解析粘贴的文本:分享链接一行一条(空行和 # 开头的行跳过),也认 base64 编码的一整段和 sing-box 的节点 JSON。
func ParseAll(text string) ([]map[string]any, []LineError) {
	nodes := []map[string]any{}
	var errs []LineError
	if t := strings.TrimSpace(text); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		return parseJSON(t)
	}
	if !strings.Contains(text, "://") { // 订阅内容常见的写法:所有链接合起来 base64 一次
		if dec, err := decodeBase64(strings.Join(strings.Fields(text), "")); err == nil && strings.Contains(dec, "://") {
			text = dec
		}
	}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		n, err := Parse(line)
		if err != nil {
			errs = append(errs, LineError{Line: i + 1, Code: CodeOf(err), Msg: err.Error()})
			continue
		}
		nodes = append(nodes, n)
	}
	return nodes, errs
}

// parseJSON sing-box 的写法:一个出站、出站数组,或带 outbounds 的整份配置。selector、direct 这些不是节点,跳过。
func parseJSON(text string) ([]map[string]any, []LineError) {
	nodes := []map[string]any{}
	var errs []LineError
	var doc any
	if json.Unmarshal([]byte(text), &doc) != nil {
		return nodes, []LineError{{Line: 1, Code: "format", Msg: "JSON 格式不对"}}
	}
	if m, ok := doc.(map[string]any); ok {
		if list, ok := m["outbounds"]; ok {
			doc = list
		} else {
			doc = []any{m}
		}
	}
	list, _ := doc.([]any)
	for i, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); notNode[t] {
			continue
		}
		n, err := Normalize(m)
		if err != nil {
			errs = append(errs, LineError{Line: i + 1, Code: CodeOf(err), Msg: err.Error()})
			continue
		}
		nodes = append(nodes, n)
	}
	if len(nodes) == 0 && len(errs) == 0 {
		errs = append(errs, LineError{Line: 1, Code: "format", Msg: "没有找到节点"})
	}
	return nodes, errs
}

// 配置里常见的、不是节点的出站。
var notNode = map[string]bool{"selector": true, "urltest": true, "direct": true, "block": true, "dns": true}

// Parse 解析一条分享链接,结果已经过 Normalize。
func Parse(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || rest == "" {
		return nil, fail("format", "不是分享链接")
	}
	var (
		n   map[string]any
		err error
	)
	switch strings.ToLower(scheme) {
	case "vmess":
		n, err = parseVmess(rest)
	case "ss":
		n, err = parseShadowsocks(rest)
	case "vless", "trojan", "hysteria2", "hy2", "tuic", "anytls", "socks", "socks5", "socks5h", "http", "https":
		u, perr := url.Parse(raw)
		if perr != nil {
			return nil, fail("format", "链接格式不对")
		}
		switch strings.ToLower(scheme) {
		case "vless":
			n, err = parseVless(u)
		case "trojan":
			n, err = parseTrojan(u)
		case "hysteria2", "hy2":
			n, err = parseHysteria2(u)
		case "tuic":
			n, err = parseTuic(u)
		case "anytls":
			n, err = parseAnytls(u)
		case "http", "https":
			n, err = parseHTTP(u)
		default:
			n, err = parseSocks(u)
		}
	default:
		return nil, fail("scheme", "不支持这种链接")
	}
	if err != nil {
		return nil, err
	}
	return Normalize(n)
}

func hostPort(u *url.URL, def int) (string, int, error) {
	host := u.Hostname()
	if host == "" {
		return "", 0, fail("server", "缺少服务器地址")
	}
	port := def
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return "", 0, fail("port", "端口不对")
		}
		port = n
	}
	return host, port, nil
}

func tagOf(u *url.URL, fallback string) string {
	if t := strings.TrimSpace(u.Fragment); t != "" {
		return t
	}
	return fallback
}

func flag(q url.Values, keys ...string) bool {
	for _, k := range keys {
		switch strings.ToLower(q.Get(k)) {
		case "1", "true", "yes":
			return true
		}
	}
	return false
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func splitList(s string) []any {
	out := []any{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// tlsOf 基于 sni / alpn / insecure / fp 组装出站 TLS。QUIC 协议的 ALPN 缺省 h3。
func tlsOf(q url.Values, host, defaultALPN string) map[string]any {
	t := map[string]any{"enabled": true, "server_name": first(q.Get("sni"), q.Get("peer"), host)}
	if a := q.Get("alpn"); a != "" {
		t["alpn"] = splitList(a)
	} else if defaultALPN != "" {
		t["alpn"] = []any{defaultALPN}
	}
	if flag(q, "insecure", "allowInsecure", "allow_insecure", "skip-cert-verify") {
		t["insecure"] = true
	}
	if fp := q.Get("fp"); fp != "" {
		t["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
	}
	return t
}

// v2rayTLS security=tls|reality 这一派的写法。forceTLS 给缺省就是 TLS 的协议(trojan)。
func v2rayTLS(q url.Values, host string, forceTLS bool) map[string]any {
	sec := strings.ToLower(q.Get("security"))
	if sec == "" && forceTLS {
		sec = "tls"
	}
	if sec != "tls" && sec != "reality" {
		return nil
	}
	t := tlsOf(q, host, "")
	if sec == "reality" {
		t["reality"] = map[string]any{"enabled": true, "public_key": q.Get("pbk"), "short_id": q.Get("sid")}
		if _, ok := t["utls"]; !ok {
			t["utls"] = map[string]any{"enabled": true, "fingerprint": "chrome"}
		}
	}
	return t
}

func transportOf(q url.Values) map[string]any {
	host, path := q.Get("host"), q.Get("path")
	switch strings.ToLower(q.Get("type")) {
	case "ws":
		tr := map[string]any{"type": "ws", "path": first(path, "/")}
		if host != "" {
			tr["headers"] = map[string]any{"Host": host}
		}
		return tr
	case "grpc":
		return map[string]any{"type": "grpc", "service_name": q.Get("serviceName")}
	case "httpupgrade":
		tr := map[string]any{"type": "httpupgrade", "path": first(path, "/")}
		if host != "" {
			tr["host"] = host
		}
		return tr
	case "http", "h2":
		tr := map[string]any{"type": "http", "path": first(path, "/")}
		if host != "" {
			tr["host"] = splitList(host)
		}
		return tr
	case "tcp", "":
		if strings.EqualFold(q.Get("headerType"), "http") {
			tr := map[string]any{"type": "http", "path": first(path, "/")}
			if host != "" {
				tr["host"] = splitList(host)
			}
			return tr
		}
	}
	return nil
}

func parseVless(u *url.URL) (map[string]any, error) {
	host, port, err := hostPort(u, 443)
	if err != nil {
		return nil, err
	}
	if u.User == nil || u.User.Username() == "" {
		return nil, fail("uuid", "缺少 UUID")
	}
	q := u.Query()
	n := map[string]any{"type": "vless", "tag": tagOf(u, host), "server": host, "server_port": port, "uuid": u.User.Username()}
	if f := q.Get("flow"); f != "" {
		n["flow"] = f
	}
	if t := v2rayTLS(q, host, false); t != nil {
		n["tls"] = t
	}
	if tr := transportOf(q); tr != nil {
		n["transport"] = tr
	}
	return n, nil
}

func parseTrojan(u *url.URL) (map[string]any, error) {
	host, port, err := hostPort(u, 443)
	if err != nil {
		return nil, err
	}
	if u.User == nil || u.User.Username() == "" {
		return nil, fail("password", "缺少密码")
	}
	q := u.Query()
	n := map[string]any{"type": "trojan", "tag": tagOf(u, host), "server": host, "server_port": port, "password": u.User.Username()}
	if t := v2rayTLS(q, host, true); t != nil {
		n["tls"] = t
	}
	if tr := transportOf(q); tr != nil {
		n["transport"] = tr
	}
	return n, nil
}

func parseHysteria2(u *url.URL) (map[string]any, error) {
	host, port, err := hostPort(u, 443)
	if err != nil {
		return nil, err
	}
	password := ""
	if u.User != nil {
		password = u.User.Username()
		if pw, ok := u.User.Password(); ok && pw != "" { // 有的客户端把口令写成 user:pass
			password += ":" + pw
		}
	}
	q := u.Query()
	n := map[string]any{"type": "hysteria2", "tag": tagOf(u, host), "server": host, "server_port": port, "password": password, "tls": tlsOf(q, host, "h3")}
	if v, err := strconv.Atoi(q.Get("upmbps")); err == nil && v > 0 {
		n["up_mbps"] = v
	}
	if v, err := strconv.Atoi(q.Get("downmbps")); err == nil && v > 0 {
		n["down_mbps"] = v
	}
	if q.Get("obfs") == "salamander" {
		n["obfs"] = map[string]any{"type": "salamander", "password": first(q.Get("obfs-password"), q.Get("obfs_password"))}
	}
	if m := q.Get("mport"); m != "" {
		n["server_ports"] = splitList(strings.ReplaceAll(m, "-", ":"))
	}
	return n, nil
}

func parseTuic(u *url.URL) (map[string]any, error) {
	host, port, err := hostPort(u, 443)
	if err != nil {
		return nil, err
	}
	if u.User == nil || u.User.Username() == "" {
		return nil, fail("uuid", "缺少 UUID")
	}
	password, _ := u.User.Password()
	q := u.Query()
	n := map[string]any{"type": "tuic", "tag": tagOf(u, host), "server": host, "server_port": port, "uuid": u.User.Username(), "password": password,
		"congestion_control": first(q.Get("congestion_control"), q.Get("congestion-control"), "cubic"), "tls": tlsOf(q, host, "h3")}
	if m := first(q.Get("udp_relay_mode"), q.Get("udp-relay-mode")); m != "" {
		n["udp_relay_mode"] = m
	}
	if flag(q, "zero_rtt_handshake", "reduce_rtt", "reduce-rtt") {
		n["zero_rtt_handshake"] = true
	}
	return n, nil
}

func parseAnytls(u *url.URL) (map[string]any, error) {
	host, port, err := hostPort(u, 443)
	if err != nil {
		return nil, err
	}
	password := ""
	if u.User != nil {
		password = u.User.Username()
	}
	return map[string]any{"type": "anytls", "tag": tagOf(u, host), "server": host, "server_port": port, "password": password, "tls": tlsOf(u.Query(), host, "")}, nil
}

func parseSocks(u *url.URL) (map[string]any, error) {
	host, port, err := hostPort(u, 1080)
	if err != nil {
		return nil, err
	}
	n := map[string]any{"type": "socks", "tag": tagOf(u, host), "server": host, "server_port": port, "version": "5"}
	if u.User != nil && u.User.Username() != "" {
		user := u.User.Username()
		pw, hasPw := u.User.Password()
		if !hasPw { // v2rayN 的写法:用户名密码整段 base64
			if dec, err := decodeBase64(user); err == nil && strings.Contains(dec, ":") {
				user, pw, _ = strings.Cut(dec, ":")
			}
		}
		n["username"], n["password"] = user, pw
	}
	return n, nil
}

// http://user:pass@host:port 是代理,带路径或没写端口的多半是把网址或订阅地址贴错了地方。
func parseHTTP(u *url.URL) (map[string]any, error) {
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Port() == "" {
		return nil, fail("subscription", "这像是网址或订阅地址,不是节点;订阅地址请用「添加订阅」")
	}
	host, port, err := hostPort(u, 0)
	if err != nil {
		return nil, err
	}
	n := map[string]any{"type": "http", "tag": tagOf(u, host), "server": host, "server_port": port}
	if u.User != nil && u.User.Username() != "" {
		pw, _ := u.User.Password()
		n["username"], n["password"] = u.User.Username(), pw
	}
	if strings.EqualFold(u.Scheme, "https") {
		n["tls"] = map[string]any{"enabled": true, "server_name": host}
	}
	return n, nil
}

// ss://base64(method:password)@host:port#name、ss://method:password@host:port#name、ss://base64(method:password@host:port)#name
func parseShadowsocks(rest string) (map[string]any, error) {
	body, frag, _ := strings.Cut(rest, "#")
	tag, _ := url.PathUnescape(frag)
	body, query, _ := strings.Cut(body, "?")
	if !strings.Contains(body, "@") {
		dec, err := decodeBase64(body)
		if err != nil || !strings.Contains(dec, "@") {
			return nil, fail("format", "链接格式不对")
		}
		body = dec
	}
	i := strings.LastIndex(body, "@")
	user, addr := body[:i], body[i+1:]
	var method, password string
	if m, p, ok := strings.Cut(user, ":"); ok {
		method, _ = url.PathUnescape(m)
		password, _ = url.PathUnescape(p)
	} else {
		unescaped, _ := url.PathUnescape(user)
		dec, err := decodeBase64(unescaped)
		if err != nil {
			return nil, fail("format", "链接格式不对")
		}
		method, password, _ = strings.Cut(dec, ":")
	}
	if method == "" || password == "" {
		return nil, fail("password", "缺少加密方式或密码")
	}
	u, err := url.Parse("ss://" + strings.TrimRight(addr, "/"))
	if err != nil {
		return nil, fail("format", "链接格式不对")
	}
	host, port, err := hostPort(u, 8388)
	if err != nil {
		return nil, err
	}
	n := map[string]any{"type": "shadowsocks", "tag": first(strings.TrimSpace(tag), host), "server": host, "server_port": port, "method": method, "password": password}
	if q, err := url.ParseQuery(query); err == nil && q.Get("plugin") != "" {
		name, opts, _ := strings.Cut(q.Get("plugin"), ";")
		n["plugin"], n["plugin_opts"] = name, opts
	}
	return n, nil
}

// vmess://base64({v,ps,add,port,id,aid,scy,net,type,host,path,tls,sni,alpn,fp})
func parseVmess(payload string) (map[string]any, error) {
	dec, err := decodeBase64(payload)
	if err != nil {
		return nil, fail("format", "链接格式不对")
	}
	var v map[string]any
	if json.Unmarshal([]byte(dec), &v) != nil {
		return nil, fail("format", "链接格式不对")
	}
	str := func(k string) string {
		switch x := v[k].(type) {
		case string:
			return x
		case float64:
			return strconv.Itoa(int(x))
		}
		return ""
	}
	host, id := str("add"), str("id")
	if host == "" {
		return nil, fail("server", "缺少服务器地址")
	}
	if id == "" {
		return nil, fail("uuid", "缺少 UUID")
	}
	port, err := strconv.Atoi(str("port"))
	if err != nil {
		return nil, fail("port", "端口不对")
	}
	aid, _ := strconv.Atoi(first(str("aid"), "0"))
	n := map[string]any{"type": "vmess", "tag": first(str("ps"), host), "server": host, "server_port": port, "uuid": id, "security": first(str("scy"), "auto"), "alter_id": aid}
	q := url.Values{}
	q.Set("type", str("net"))
	q.Set("host", str("host"))
	q.Set("path", str("path"))
	if strings.EqualFold(str("type"), "http") {
		q.Set("headerType", "http")
	}
	if str("net") == "grpc" {
		q.Set("serviceName", str("path"))
	}
	if tr := transportOf(q); tr != nil {
		n["transport"] = tr
	}
	if str("tls") == "tls" {
		tq := url.Values{}
		tq.Set("sni", first(str("sni"), str("host")))
		tq.Set("alpn", str("alpn"))
		tq.Set("fp", str("fp"))
		if s := str("allowInsecure"); s == "1" || s == "true" {
			tq.Set("insecure", "1")
		}
		n["tls"] = tlsOf(tq, host, "")
	}
	return n, nil
}

func decodeBase64(s string) (string, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), nil
		}
	}
	return "", fmt.Errorf("not base64")
}

// 节点允许的字段。别的一律丢掉:detour、domain_resolver 这些指着配置里别的东西,留着节点就建不起来。
var allowedKeys = map[string]bool{"type": true, "tag": true, "server": true, "server_port": true, "server_ports": true, "hop_interval": true,
	"uuid": true, "password": true, "username": true, "method": true, "flow": true, "alter_id": true, "security": true, "version": true,
	"congestion_control": true, "udp_relay_mode": true, "zero_rtt_handshake": true, "up_mbps": true, "down_mbps": true, "obfs": true,
	"tls": true, "transport": true, "plugin": true, "plugin_opts": true}

// 这些协议要 TLS。
var tlsRequired = map[string]bool{"hysteria2": true, "tuic": true, "anytls": true, "trojan": true}

var hostRe = regexp.MustCompile(`^[\p{L}\p{N}._-]+$`)

// Types 支持手动添加的协议。
var Types = []string{"vless", "vmess", "trojan", "shadowsocks", "hysteria2", "tuic", "anytls", "socks", "http"}

// Normalize 检查并整理一个节点:必填项、地址端口、去掉不认识的字段、补上 TLS 的 SNI。参数有没有真的能用由内核校验(core.Unbuildable)。
func Normalize(in map[string]any) (map[string]any, error) {
	n := make(map[string]any, len(in))
	for k, v := range in {
		if allowedKeys[k] && v != nil && v != "" {
			n[k] = v
		}
	}
	typ, _ := n["type"].(string)
	known := false
	for _, t := range Types {
		known = known || t == typ
	}
	if !known {
		return nil, fail("scheme", "不支持这种协议")
	}
	server, _ := n["server"].(string)
	server = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(server), "["), "]")
	if server == "" {
		return nil, fail("server", "缺少服务器地址")
	}
	if net.ParseIP(server) == nil && !hostRe.MatchString(server) {
		return nil, fail("server", "服务器地址不对")
	}
	n["server"] = server
	port, ok := toInt(n["server_port"])
	if !ok || port < 1 || port > 65535 {
		return nil, fail("port", "端口要在 1 到 65535 之间")
	}
	n["server_port"] = port
	tag, _ := n["tag"].(string)
	if tag = strings.TrimSpace(tag); tag == "" {
		tag = server
	}
	n["tag"] = tag

	need := func(key, code, msg string) error {
		if s, _ := n[key].(string); strings.TrimSpace(s) == "" {
			return fail(code, msg)
		}
		return nil
	}
	var err error
	switch typ {
	case "vless", "tuic":
		err = need("uuid", "uuid", "缺少 UUID")
	case "vmess":
		err = need("uuid", "uuid", "缺少 UUID")
		if _, ok := n["security"]; !ok {
			n["security"] = "auto"
		}
	case "trojan", "hysteria2", "anytls":
		err = need("password", "password", "缺少密码")
	case "shadowsocks":
		if err = need("method", "method", "缺少加密方式"); err == nil {
			err = need("password", "password", "缺少密码")
		}
	case "socks":
		n["version"] = "5"
	}
	if err != nil {
		return nil, err
	}

	if t, ok := n["tls"].(map[string]any); ok && t["enabled"] != false {
		t["enabled"] = true
		if s, _ := t["server_name"].(string); s == "" {
			t["server_name"] = server
		}
		if typ == "hysteria2" || typ == "tuic" { // 这两种跑在 QUIC 上:服务端都要 h3,也用不了 uTLS(内核拨号时才报错)
			if t["alpn"] == nil {
				t["alpn"] = []any{"h3"}
			}
			delete(t, "utls")
		}
		if r, ok := t["reality"].(map[string]any); ok && r["enabled"] == true {
			if k, _ := r["public_key"].(string); k == "" {
				return nil, fail("pbk", "Reality 缺少公钥")
			}
			if _, ok := t["utls"]; !ok { // Reality 离不开 uTLS,内核要求
				t["utls"] = map[string]any{"enabled": true, "fingerprint": "chrome"}
			}
		}
	} else if tlsRequired[typ] {
		t := map[string]any{"enabled": true, "server_name": server}
		if typ == "hysteria2" || typ == "tuic" {
			t["alpn"] = []any{"h3"}
		}
		n["tls"] = t
	} else {
		delete(n, "tls")
	}
	return n, nil
}

func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case float64:
		return int(x), x == float64(int(x))
	case json.Number:
		i, err := x.Int64()
		return int(i), err == nil
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(x))
		return i, err == nil
	}
	return 0, false
}
