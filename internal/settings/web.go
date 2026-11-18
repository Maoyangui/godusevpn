package settings

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"regexp"
	"runtime"
	"strings"
)

// Web 面板(Linux):监听地址与密码。密码不明文存,存 salt$sha256(salt+密码)。

// defaultWebListen Linux(软路由 / 云主机)默认就对局域网开放,安装时会生成随机密码并打印;
// 其它平台面板只给本机看(Windows 有客户端窗口,Android 没有面板)。
func defaultWebListen() string {
	if runtime.GOOS == "linux" {
		return "0.0.0.0:9800"
	}
	return "127.0.0.1:9800"
}

func (s *Settings) validateWeb() error {
	hosts, err := normWebHosts(s.WebHosts)
	if err != nil {
		return err
	}
	s.WebHosts = hosts
	s.WebListen = strings.TrimSpace(s.WebListen)
	if s.WebListen == "" {
		return nil
	}
	_, port, err := net.SplitHostPort(s.WebListen)
	if err != nil {
		return fmt.Errorf("面板监听地址无效: %q(如 127.0.0.1:9800 或 0.0.0.0:9800)", s.WebListen)
	}
	if port == "0" || port == "" {
		return errors.New("面板端口无效")
	}
	// 没设密码不算设置无效(默认值就是这样,Linux 安装时才生成密码):
	// 面板自己会拒绝访问并提示去设密码,见 internal/web。
	return nil
}

// normWebHosts 规范设置 webHosts:去空格、转小写、去掉末尾的点;每一项要是域名(可带 *. 前缀)或者单独一个 *。
func normWebHosts(v string) (string, error) {
	var out []string
	for _, h := range strings.Split(v, ",") {
		h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		if h == "" {
			continue
		}
		if h != "*" && !hostnameRe.MatchString(strings.TrimPrefix(h, "*.")) {
			return "", fmt.Errorf("面板域名 %q 不对:只写域名,多个用逗号隔开(不带 http:// 和端口),如 panel.example.com", h)
		}
		out = append(out, h)
	}
	return strings.Join(out, ","), nil
}

var hostnameRe = regexp.MustCompile(`^[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?)*$`)

func loopback(host string) bool {
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// WebPublic 面板是否对外(非回环)监听。
func (s Settings) WebPublic() bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(s.WebListen))
	return err == nil && !loopback(host)
}

// SetWebPassword 设密码;空串 = 清除。
func (s *Settings) SetWebPassword(plain string) error {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		s.WebPassword = ""
		return nil
	}
	if len(plain) < 6 {
		return errors.New("密码至少 6 位")
	}
	salt := make([]byte, 8)
	_, _ = rand.Read(salt)
	s.WebPassword = hex.EncodeToString(salt) + "$" + hashPassword(salt, plain)
	return nil
}

// CheckWebPassword 校验;没设密码时一律不通过(面板那时不给用)。
func (s Settings) CheckWebPassword(plain string) bool {
	if s.WebPassword == "" {
		return false
	}
	saltHex, want, ok := strings.Cut(s.WebPassword, "$")
	if !ok {
		return false
	}
	salt, err := hex.DecodeString(saltHex)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hashPassword(salt, plain)), []byte(want)) == 1
}

func hashPassword(salt []byte, plain string) string {
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(plain))
	return hex.EncodeToString(h.Sum(nil))
}

// RandomPassword 安装时生成的初始密码:12 位字母数字。
func RandomPassword() string {
	const chars = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}
