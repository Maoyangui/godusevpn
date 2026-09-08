// Package settings 客户端本地设置:订阅列表、模式、TUN、DNS、IPv6、端口、测速间隔等。带 schema 版本,升级时迁移。
// 订阅内容(节点)是缓存,另存;这里只记地址与名字。
package settings

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
)

const Schema = 2

const (
	ModeRule   = "rule"
	ModeGlobal = "global"
	ModeDirect = "direct"
)

// Profile 一条订阅。
type Profile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Settings struct {
	Schema        int       `json:"schema"`
	Profiles      []Profile `json:"profiles"`
	ActiveProfile string    `json:"activeProfile"`        // 当前用的订阅 id
	ProfileURL    string    `json:"profileUrl,omitempty"` // schema 1 遗留,加载时迁成 Profiles
	Mode          string    `json:"mode"`                 // rule | global | direct
	TUN           bool      `json:"tun"`                  // TUN 模式(默认开);关掉只留混合端口
	TUNStack      string    `json:"tunStack"`             // mixed | system | gvisor
	StrictRoute   bool      `json:"strictRoute"`          // 严格路由:防泄漏,代价是部分局域网访问要靠 lanBypass
	LANBypass     bool      `json:"lanBypass"`            // 私网段不进 TUN(打印机、NAS 直通)
	MixedPort     int       `json:"mixedPort"`            // 本机混合端口,0 = 关
	RemoteDNS     string    `json:"remoteDns"`            // 经代理的 DoH 服务器(IP 或域名)
	LocalDNS      string    `json:"localDns"`             // 直连的 DoH 服务器;"system" = 用系统 DNS
	FakeIP        bool      `json:"fakeIp"`
	IPv6          bool      `json:"ipv6"` // false = 全链路禁用
	AdBlock       bool      `json:"adBlock"`
	UpdateHours   int       `json:"updateHours"`  // 订阅刷新间隔(小时)
	ProbeMinutes  int       `json:"probeMinutes"` // 定时测速:auto 组每隔多少分钟测一轮全部节点(1 到 60)
	LogLevel      string    `json:"logLevel"`     // debug | info | warn | error
	LogDays       int       `json:"logDays"`      // 日志保留天数,超过自动删除;0 = 一直保留
	WebListen     string    `json:"webListen"`    // Web 面板监听地址(Linux),如 127.0.0.1:9800 / 0.0.0.0:9800;空 = 不开面板
	WebPassword   string    `json:"webPassword"`  // 面板密码的加盐哈希(salt$sha256);空 = 无密码,此时只允许监听回环地址
	NetMode       string    `json:"netMode"`      // local | gateway(Linux 软路由:代理经本机转发的局域网流量),见 gateway.go
	LANSubnets    []string  `json:"lanSubnets"`   // 网关模式下的局域网网段;空 = 自动
	DNSHijack     bool      `json:"dnsHijack"`    // 网关模式下劫持局域网设备的 DNS
	Devices       []Device  `json:"devices"`      // 局域网设备策略
	ClashPort     int       `json:"clashPort"`    // 内核 Clash API 端口(只监听回环)
	Selected      string    `json:"selected"`     // proxy 组当前选中的节点;空 = auto
	// BypassApps 这些进程(如 steam.exe)的流量不走代理,直连出去;按进程名匹配,不分大小写
	BypassApps []string `json:"bypassApps"`
	// RuleGroups 用户自定义规则组,按顺序匹配,排在内置默认规则之前(见 rules.go)
	RuleGroups []RuleGroup `json:"ruleGroups"`
}

// Default 出厂默认:TUN + 规则模式 + DoH + fake-ip + 禁 IPv6。
func Default() Settings {
	return Settings{
		Schema: Schema, Mode: ModeRule, TUN: true, TUNStack: "mixed", StrictRoute: true, LANBypass: true,
		MixedPort: 2080, RemoteDNS: "1.1.1.1", LocalDNS: "223.5.5.5", FakeIP: true, IPv6: false,
		UpdateHours: 6, ProbeMinutes: 3, LogLevel: "info", LogDays: 7, ClashPort: 9090, WebListen: defaultWebListen(),
		NetMode: NetLocal, DNSHijack: true,
	}
}

// NewID 订阅 id:8 字节随机十六进制。
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
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
	if s.Schema > Schema {
		return Default(), fmt.Errorf("设置文件来自更新的版本(schema %d),请升级客户端", s.Schema)
	}
	s.migrate()
	if err := s.Validate(); err != nil {
		return Default(), err
	}
	return s, nil
}

// migrate schema 1 → 2:单个 profileUrl 变成订阅列表。
func (s *Settings) migrate() {
	if s.ProfileURL != "" && len(s.Profiles) == 0 {
		id := NewID()
		s.Profiles = []Profile{{ID: id, Name: "默认", URL: s.ProfileURL}}
		s.ActiveProfile = id
	}
	s.ProfileURL = ""
	if s.ProbeMinutes == 0 {
		s.ProbeMinutes = 3
	}
	s.Schema = Schema
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

// Active 当前订阅(没有返回 nil)。
func (s Settings) Active() *Profile {
	for i := range s.Profiles {
		if s.Profiles[i].ID == s.ActiveProfile {
			return &s.Profiles[i]
		}
	}
	return nil
}

// ValidURL 订阅地址是否合法。
func ValidURL(u string) bool {
	l := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
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
	if s.ProbeMinutes < 1 || s.ProbeMinutes > 60 {
		return errors.New("测速间隔须在 1 到 60 分钟之间")
	}
	switch s.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("日志级别无效: %q", s.LogLevel)
	}
	if s.LogDays < 0 || s.LogDays > 365 {
		return errors.New("日志保留天数须在 0 到 365 之间(0 = 一直保留)")
	}
	if err := s.validateWeb(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i := range s.Profiles {
		p := &s.Profiles[i]
		p.Name, p.URL = strings.TrimSpace(p.Name), strings.TrimSpace(p.URL)
		if p.ID == "" || seen[p.ID] {
			return errors.New("订阅 id 重复或为空")
		}
		seen[p.ID] = true
		if !ValidURL(p.URL) {
			return fmt.Errorf("订阅「%s」的地址必须以 http:// 或 https:// 开头", p.Name)
		}
		if p.Name == "" {
			p.Name = fmt.Sprintf("订阅 %d", i+1)
		}
	}
	if len(s.Profiles) > 0 && !seen[s.ActiveProfile] {
		s.ActiveProfile = s.Profiles[0].ID
	}
	if len(s.Profiles) == 0 {
		s.ActiveProfile = ""
	}
	var apps []string
	for _, a := range s.BypassApps {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if strings.ContainsAny(a, "/\\*?<>|\"") || strings.Contains(a, string(os.PathSeparator)) {
			return fmt.Errorf("进程名无效: %q(只写文件名,如 steam.exe)", a)
		}
		apps = append(apps, a)
	}
	s.BypassApps = apps
	if err := s.validateGateway(); err != nil {
		return err
	}
	return s.validateRules()
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

// Clone 深拷贝:切片字段都换成新的底层数组,拿到副本的人随便改也不会影响原设置。
func (s Settings) Clone() Settings {
	c := s
	c.Profiles = slices.Clone(s.Profiles)
	c.LANSubnets = slices.Clone(s.LANSubnets)
	c.Devices = slices.Clone(s.Devices)
	c.BypassApps = slices.Clone(s.BypassApps)
	c.RuleGroups = slices.Clone(s.RuleGroups)
	for i := range c.RuleGroups {
		c.RuleGroups[i].Rules = slices.Clone(c.RuleGroups[i].Rules)
	}
	return c
}
