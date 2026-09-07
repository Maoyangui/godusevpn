// Package settings 客户端本地设置:模式、TUN、DNS、IPv6、端口、自启等。带 schema 版本,升级时迁移。
// 订阅地址也在这里(它是"设置",节点内容是"订阅缓存",两者分开存)。
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
)

const Schema = 1

const (
	ModeRule   = "rule"
	ModeGlobal = "global"
	ModeDirect = "direct"
)

type Settings struct {
	Schema      int    `json:"schema"`
	ProfileURL  string `json:"profileUrl"`
	Mode        string `json:"mode"`        // rule | global | direct
	TUN         bool   `json:"tun"`         // TUN 模式(默认开);关掉只留混合端口
	TUNStack    string `json:"tunStack"`    // mixed | system | gvisor
	StrictRoute bool   `json:"strictRoute"` // 严格路由:防泄漏,代价是部分局域网访问要靠 lanBypass
	LANBypass   bool   `json:"lanBypass"`   // 私网段不进 TUN(打印机、NAS 直通)
	MixedPort   int    `json:"mixedPort"`   // 本机混合端口,0 = 关
	RemoteDNS   string `json:"remoteDns"`   // 经代理的 DoH 服务器(IP 或域名)
	LocalDNS    string `json:"localDns"`    // 直连的 DoH 服务器;"system" = 用系统 DNS
	FakeIP      bool   `json:"fakeIp"`
	IPv6        bool   `json:"ipv6"` // false = 全链路禁用
	AdBlock     bool   `json:"adBlock"`
	UpdateHours int    `json:"updateHours"` // 订阅刷新间隔(小时)
	LogLevel    string `json:"logLevel"`    // debug | info | warn | error
	ClashPort   int    `json:"clashPort"`   // 内核 Clash API 端口(只监听回环)
	Selected    string `json:"selected"`    // proxy 组当前选中的节点;空 = auto
}

// Default 出厂默认:TUN + 规则模式 + DoH + fake-ip + 禁 IPv6。
func Default() Settings {
	return Settings{
		Schema: Schema, Mode: ModeRule, TUN: true, TUNStack: "mixed", StrictRoute: true, LANBypass: true,
		MixedPort: 2080, RemoteDNS: "1.1.1.1", LocalDNS: "223.5.5.5", FakeIP: true, IPv6: false,
		UpdateHours: 6, LogLevel: "info", ClashPort: 9090,
	}
}

// Load 读设置;文件不存在给默认值。旧 schema 在这里迁移。
func Load(path string) (Settings, error) {
	s := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return Default(), fmt.Errorf("设置文件损坏: %w", err)
	}
	if s.Schema == 0 {
		s.Schema = Schema
	}
	if s.Schema > Schema {
		return Default(), fmt.Errorf("设置文件来自更新的版本(schema %d),请升级客户端", s.Schema)
	}
	if err := s.Validate(); err != nil {
		return Default(), err
	}
	return s, nil
}

func (s Settings) Save(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Validate 检查取值范围;界面与命令行改设置都走它。
func (s *Settings) Validate() error {
	switch s.Mode {
	case ModeRule, ModeGlobal, ModeDirect:
	default:
		return fmt.Errorf("模式无效: %q(rule / global / direct)", s.Mode)
	}
	switch s.TUNStack {
	case "mixed", "system", "gvisor":
	default:
		return fmt.Errorf("TUN 协议栈无效: %q(mixed / system / gvisor)", s.TUNStack)
	}
	if s.MixedPort < 0 || s.MixedPort > 65535 {
		return errors.New("混合端口超出范围")
	}
	if !s.TUN && s.MixedPort == 0 {
		return errors.New("TUN 关闭时必须开混合端口,否则没有任何入口")
	}
	if s.ClashPort < 1 || s.ClashPort > 65535 {
		return errors.New("控制端口超出范围")
	}
	if s.MixedPort == s.ClashPort {
		return errors.New("混合端口与控制端口不能相同")
	}
	if !validHost(s.RemoteDNS) {
		return fmt.Errorf("远程 DNS 无效: %q", s.RemoteDNS)
	}
	if s.LocalDNS != "system" && !validHost(s.LocalDNS) {
		return fmt.Errorf("本地 DNS 无效: %q", s.LocalDNS)
	}
	if s.UpdateHours < 1 || s.UpdateHours > 168 {
		return errors.New("订阅刷新间隔须在 1 到 168 小时之间")
	}
	switch s.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("日志级别无效: %q", s.LogLevel)
	}
	if strings.TrimSpace(s.ProfileURL) != "" && !strings.HasPrefix(strings.ToLower(s.ProfileURL), "http://") && !strings.HasPrefix(strings.ToLower(s.ProfileURL), "https://") {
		return errors.New("订阅地址必须以 http:// 或 https:// 开头")
	}
	return nil
}

// validHost IP 或看起来像域名的字符串。
func validHost(h string) bool {
	h = strings.TrimSpace(h)
	if h == "" {
		return false
	}
	if net.ParseIP(h) != nil {
		return true
	}
	for _, c := range h {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-') {
			return false
		}
	}
	return strings.Contains(h, ".")
}

// IsIP 主机是否为 IP 字面量(DoH 服务器是域名时要先给它配解析器)。
func IsIP(h string) bool { return net.ParseIP(strings.TrimSpace(h)) != nil }

// Apply 按 key=value 改一项(命令行用),返回改后的副本;不认识的键报错。
func (s Settings) Apply(key, value string) (Settings, error) {
	b, _ := json.Marshal(s)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	if _, ok := m[key]; !ok {
		return s, fmt.Errorf("没有这个设置项: %s", key)
	}
	var v any
	switch strings.ToLower(value) {
	case "true", "on", "yes":
		v = true
	case "false", "off", "no":
		v = false
	default:
		var n int
		if _, err := fmt.Sscanf(value, "%d", &n); err == nil && fmt.Sprint(n) == value {
			v = n
		} else {
			v = value
		}
	}
	vb, _ := json.Marshal(v)
	m[key] = vb
	nb, _ := json.Marshal(m)
	out := s
	if err := json.Unmarshal(nb, &out); err != nil {
		return s, fmt.Errorf("%s 的值类型不对: %w", key, err)
	}
	if err := out.Validate(); err != nil {
		return s, err
	}
	return out, nil
}
