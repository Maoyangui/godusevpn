package settings

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Web 面板(Linux):监听地址与密码。密码不明文存,存 salt$sha256(salt+密码)。

func (s *Settings) validateWeb() error {
	s.WebListen = strings.TrimSpace(s.WebListen)
	if s.WebListen == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(s.WebListen)
	if err != nil {
		return fmt.Errorf("面板监听地址无效: %q(如 127.0.0.1:9800 或 0.0.0.0:9800)", s.WebListen)
	}
	if port == "0" || port == "" {
		return errors.New("面板端口无效")
	}
	if !loopback(host) && s.WebPassword == "" {
		return errors.New("面板监听非回环地址时必须设置密码")
	}
	return nil
}

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

// CheckWebPassword 校验;没设密码时任何输入都通过(此时只可能监听回环)。
func (s Settings) CheckWebPassword(plain string) bool {
	if s.WebPassword == "" {
		return true
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
