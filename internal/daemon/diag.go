package daemon

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/logx"
	"github.com/Maoyangui/godusevpn/internal/paths"
)

// 诊断包:日志、脱敏后的配置与设置、路由表、网卡信息。凭据类字段一律打码,订阅地址只留主机名。
var secretKeys = map[string]bool{"password": true, "uuid": true, "privatekey": true, "psk": true, "presharedkey": true, "secret": true, "token": true, "credential": true, "credentials": true, "auth": true, "authstr": true, "authorization": true, "proxyauthorization": true, "apikey": true}

func secretKey(key string) bool {
	key = strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(key))
	return secretKeys[key] || strings.HasSuffix(key, "password") || strings.HasSuffix(key, "token") || strings.HasSuffix(key, "secret")
}

func redact(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if secretKey(k) {
				out[k] = "***"
			} else if strings.HasSuffix(strings.ToLower(k), "url") {
				if s, ok := val.(string); ok {
					out[k] = redactURL(s)
				} else {
					out[k] = redact(val)
				}
			} else {
				out[k] = redact(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = redact(x[i])
		}
		return out
	case string:
		return redactText(x)
	default:
		return v
	}
}

var (
	diagURL       = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s<>"'\x60]+`)
	subPath       = regexp.MustCompile(`(?i)(/sub/)[^\s/?#"'<>]+`)
	bearerToken   = regexp.MustCompile(`(?i)\bBearer[ \t]+[^\s,"'<>]+`)
	logCredential = regexp.MustCompile(`(?i)\b((?:access[_-]?|refresh[_-]?)?token|(?:web[_-]?|obfs[_-]?|proxy[_-]?)?password|client[_-]?secret|private[_-]?key|api[_-]?key|authorization|uuid|psk)([ \t]*[:=][ \t]*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;]+)`)
)

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		if raw == "" {
			return ""
		}
		return "***" // malformed or opaque subscription links must not echo credentials
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "ws", "wss", "ftp", "socks", "socks5", "trojan", "ss", "vless", "hysteria", "hysteria2", "tuic":
		return (&url.URL{Scheme: strings.ToLower(u.Scheme), Host: u.Host}).String()
	default:
		// vmess and other opaque URI schemes can encode the entire credential
		// into what url.Parse considers a hostname.
		return "***"
	}
}

func redactText(s string) string {
	// Logs may include JSON-escaped URLs. Normalization only affects the
	// exported copy, never the underlying log or settings file.
	s = strings.ReplaceAll(s, `\/`, `/`)
	s = diagURL.ReplaceAllStringFunc(s, redactURL)
	s = bearerToken.ReplaceAllString(s, "Bearer ***")
	s = logCredential.ReplaceAllString(s, "${1}${2}***")
	return subPath.ReplaceAllString(s, "${1}***")
}

func cmdOut(name string, args ...string) string {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("(%s %s: %v)\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// exportDiag 生成 zip,返回路径。
func (d *Daemon) exportDiag() (string, error) {
	dir := filepath.Join(paths.DataDir(), "diag")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "godusevpn-diag-"+time.Now().Format("20060102-150405")+".zip")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	add := func(name, content string) {
		w, err := zw.Create(name)
		if err == nil {
			_, _ = w.Write([]byte(redactText(content)))
		}
	}
	view := d.stateView()
	info := map[string]any{"version": buildinfo.Version, "os": runtime.GOOS + "/" + runtime.GOARCH, "time": time.Now().Format(time.RFC3339), "state": view}
	if _, p := d.activeProfile(); p != nil {
		info["servers"] = p.Servers()
	}
	// Marshal into a detached JSON tree before redaction: state contains
	// structs and slices that may share backing data with the live settings.
	b, err := json.Marshal(info)
	if err != nil {
		return "", err
	}
	var detached any
	if err := json.Unmarshal(b, &detached); err != nil {
		return "", err
	}
	b, err = json.MarshalIndent(redact(detached), "", "  ")
	if err != nil {
		return "", err
	}
	add("info.json", string(b))
	if raw, err := os.ReadFile(paths.Config()); err == nil {
		var cfg any
		if json.Unmarshal(raw, &cfg) == nil {
			rb, _ := json.MarshalIndent(redact(cfg), "", "  ")
			add("config.redacted.json", string(rb))
		}
	}
	add("service.log", strings.Join(logx.Tail(d.log.Path(), 500), "\n"))
	add("core.log", strings.Join(logx.Tail(d.coreLog.Path(), 500), "\n"))
	if crash := filepath.Join(paths.Logs(), "crash.log"); func() bool { st, err := os.Stat(crash); return err == nil && st.Size() > 0 }() { // Android 上 Go / Kotlin 侧的崩溃记录
		add("crash.log", strings.Join(logx.Tail(crash, 300), "\n"))
	}
	sysDiag(add)
	if err := zw.Close(); err != nil {
		return "", err
	}
	return path, nil
}
