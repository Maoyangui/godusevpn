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
	"time"
)

const Schema = 4

// DefaultProbeMinutes 自动选择多久真连一轮全部节点。当前节点通不通另有健康检查盯着,这一轮只是为了换到更快的,不用太勤。
const DefaultProbeMinutes = 10

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
	// Manual 手动添加的节点:没有地址,不拉取,节点存在这条订阅的缓存里(见 daemon/manual.go)。
	Manual bool `json:"manual,omitempty"`
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
	// NoDirect 防流量逃逸:全局模式下只要还想连着(没点断开),任何流量都不许绕过隧道直连 —— 隧道没起来、
	// 内核在重启、节点不通、崩了在重试,统统只能等。放行的只有隧道自己(节点连接、订阅刷新的回退)、局域网、回环。
	NoDirect  bool   `json:"noDirect"`
	LANBypass bool   `json:"lanBypass"` // 私网段不进 TUN(打印机、NAS 直通)
	MixedPort int    `json:"mixedPort"` // 本机混合端口,0 = 关
	RemoteDNS string `json:"remoteDns"` // 经代理的 DoH 服务器(IP 或域名)
	LocalDNS  string `json:"localDns"`  // 直连的 DoH 服务器;"system" = 用系统 DNS
	FakeIP    bool   `json:"fakeIp"`
	IPv6      bool   `json:"ipv6"` // false = 全链路禁用
	// DisableNICIPv6 关闭 IPv6 时,顺带把各网卡上的 IPv6 协议也停用(隧道自己那张除外),断开时还原。
	// 只挡数据包挡不住这一条:运营商给的公网 IPv6 地址就配在网卡上,程序枚举一遍网卡就能读走再报出去。
	DisableNICIPv6 bool     `json:"disableNicIpv6"`
	AdBlock        bool     `json:"adBlock"`
	UpdateHours    int      `json:"updateHours"`    // 订阅刷新间隔(小时)
	ProbeMinutes   int      `json:"probeMinutes"`   // 定时测速:auto 组每隔多少分钟测一轮全部节点(1 到 60)
	ProbeMoved     bool     `json:"probeMoved"`     // 原来的默认值 3 分钟已经迁到新默认(只迁一次,见 migrate)
	LogLevel       string   `json:"logLevel"`       // debug | info | warn | error
	LogDays        int      `json:"logDays"`        // 日志保留天数,超过自动删除;0 = 一直保留
	CoreLogDisk    bool     `json:"coreLogDisk"`    // 内核日志写全到磁盘(默认关:连接目标只留在内存里,磁盘上只有打了码的警告和错误)
	NoExitLookup   bool     `json:"noExitLookup"`   // 不查出口地址(默认查:连上后经节点问第三方网站一次,首页显示出口)
	WebAllowPublic bool     `json:"webAllowPublic"` // 允许从公网打开面板(默认只给本机和局域网:面板是明文 HTTP)
	WebListen      string   `json:"webListen"`      // Web 面板监听地址(Linux),如 127.0.0.1:9800 / 0.0.0.0:9800;空 = 不开面板
	WebPassword    string   `json:"webPassword"`    // 面板密码的加盐哈希(salt$sha256);空 = 无密码,此时面板不给用
	NetMode        string   `json:"netMode"`        // local | gateway(Linux 软路由:代理经本机转发的局域网流量),见 gateway.go
	LANSubnets     []string `json:"lanSubnets"`     // 保留字段,没有接线(见 gateway.go)
	Devices        []Device `json:"devices"`        // 局域网设备策略
	ClashPort      int      `json:"clashPort"`      // 内核 Clash API 端口(只监听回环)
	Selected       string   `json:"selected"`       // proxy 组当前选中的节点或线路组;空 = auto
	// SelectedNode 选的是线路组时,线路组用不了的时候用的那一台(组里的一个成员,守护进程维护):旧版本不认识线路组,
	// 磁盘上 selected 写的就是它(见 Save);线路组在订阅里没了(线路改成只部署一台)时,这一轮连接也用它,线路不变。
	// 只在服务自己手里,不经控制口给出去。
	SelectedNode string `json:"-"`
	// SelectedGroup 只在磁盘上出现:选的是线路组时组名存在这里(旧版会忽略不认识的键),selected 写组里的一台。
	// 读进来(Load)就挪回 Selected,内存里、控制口上一直是空的。
	SelectedGroup string `json:"selectedGroup,omitempty"`
	// LineAuto 节点面板顶上的「线路自动」:同一线路有多台服务器时合成一项,选它就在这几台里自动用最快的。
	// 只是"按线路选"的偏好,本身不改内核配置(只有选中的是线路组时配置里才有线路组,见 builder);只归 SetLineAuto 管,
	// 保存设置时沿用原值。加这一项不升 schema:旧版读设置时忽略它,退回旧版也起得来(见 TestSchemaStaysReadableByOldClients)。
	LineAuto bool `json:"lineAuto"`
	// BypassApps 这些进程(如 steam.exe)的流量不走代理,直连出去;按进程名匹配,不分大小写
	BypassApps []string `json:"bypassApps"`
	// RuleGroups 用户自定义规则组,按顺序匹配,排在内置默认规则之前(见 rules.go)
	RuleGroups []RuleGroup `json:"ruleGroups"`
	// DefaultRules 内置默认规则里可改的几项(私网 / 国内 / 其余),可一键还原出厂
	DefaultRules DefaultRules `json:"defaultRules"`
	// Rev 这份设置是守护进程手里的第几版:只在控制口上出现(读设置时带出去,存设置时带回来),不落盘。
	// 存设置的一方拿着旧版本整份存回来时,守护进程只把它相对那一版改了的字段合并上去,别人这期间改的不被盖掉(见 Merge3)。
	// 0 = 没带(旧客户端),整份照存。
	Rev uint64 `json:"rev,omitempty"`
}

// Default 出厂默认:TUN + 规则模式 + DoH + fake-ip + 禁 IPv6。
func Default() Settings {
	return Settings{
		Schema: Schema, ProbeMoved: true, Mode: ModeRule, TUN: true, DisableNICIPv6: true, TUNStack: "mixed", StrictRoute: true, NoDirect: true, LANBypass: true,
		MixedPort: 2080, RemoteDNS: "1.1.1.1", LocalDNS: "223.5.5.5", FakeIP: true, IPv6: false,
		UpdateHours: 6, ProbeMinutes: DefaultProbeMinutes, LogLevel: "info", LogDays: 7, ClashPort: 9090, WebListen: defaultWebListen(),
		NetMode: NetLocal, DefaultRules: FactoryDefaultRules(),
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
	s, _, err := LoadRaw(path)
	return s, err
}

// LoadRaw 同 Load,另外给出读到的原始字节(文件不存在时为 nil):存的时候拿它判断这期间文件有没有被别的进程改过(见 SaveChecked)。
func LoadRaw(path string) (Settings, []byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), nil, nil
		}
		return Default(), nil, err
	}
	s, err := parse(b)
	return s, b, err
}

// parse 把设置文件的内容读成设置(迁移、校验);读不出来给默认值和错误。
func parse(b []byte) (Settings, error) {
	s := Default()
	s.ProbeMoved = false // 老文件没有这个键,读出来得是 false 才会迁
	if err := json.Unmarshal(b, &s); err != nil {
		return Default(), fmt.Errorf("设置文件损坏: %w", err)
	}
	if s.SelectedGroup != "" { // 选的是线路组:selected 那一台是写给旧版看的(见 Save)
		s.SelectedNode, s.Selected, s.SelectedGroup = s.Selected, s.SelectedGroup, ""
	}
	if s.Schema > Schema {
		return Default(), fmt.Errorf("设置文件来自更新的版本(schema %d),请升级客户端", s.Schema)
	}
	s.Rev = 0 // 不落盘的东西,文件里有也不认
	s.migrate()
	if err := s.Validate(); err != nil {
		return Default(), err
	}
	return s, nil
}

// migrate schema 1 → 2:单个 profileUrl 变成订阅列表。
// Sealed 严格全局:全局模式 + 防流量逃逸。隧道以外只剩节点连接本身、回环、(局域网直通开着时的)局域网。
func (s Settings) Sealed() bool { return s.NoDirect && s.Mode == ModeGlobal }

// RuleGuard 规则模式 + 防流量逃逸:防火墙照着规则放行直连,其余只许走隧道。
// 要开着 TUN:没有 TUN 时规则只管连混合端口的程序,别的程序本来就不归隧道管,不能因为这个开关断它们的网。
func (s Settings) RuleGuard() bool { return s.NoDirect && s.Mode == ModeRule && s.TUN }

// GuardOn 按设置,防火墙这道闸该不该在(还要看用户想不想连着)。
func (s Settings) GuardOn() bool { return s.Sealed() || s.RuleGuard() }

func (s *Settings) migrate() {
	if s.ProfileURL != "" && len(s.Profiles) == 0 {
		id := NewID()
		s.Profiles = []Profile{{ID: id, Name: "默认", URL: s.ProfileURL}}
		s.ActiveProfile = id
	}
	s.ProfileURL = ""
	if s.ProbeMinutes == 0 {
		s.ProbeMinutes = DefaultProbeMinutes
	}
	s.DefaultRules.normalize() // 老设置文件里没有这一段
	if s.Schema < 3 {
		s.DisableNICIPv6 = true // 这一项是后加的,老设置文件里读出来是 false,按新默认值补上
	}
	if s.Schema < 4 {
		s.NoDirect = true // 同上:防流量逃逸默认开
	}
	if !s.ProbeMoved {
		if s.ProbeMinutes == 3 {
			s.ProbeMinutes = DefaultProbeMinutes // 3 是原来的默认值,跟着新默认走;自己改过的不动
		}
		s.ProbeMoved = true
	}
	s.Schema = Schema
}

// Save 落盘(先写临时文件再改名)。
//
// 选的是线路组时,selected 写组里的一台(SelectedNode)、组名写进 selectedGroup:退回旧版本时旧版不认识线路组,
// 把组名当节点就找不到、回落自动选择,还会把它当"手动选了节点"停掉定时测速;写成组里的一台,旧版就按手动选了这台来连,
// 线路不变。不升 schema(旧版读到更新的 schema 会拒绝启动)。
//
// 和别的进程写设置互斥(见 lockFile);要"读出来、改几项、存回去"的用 Update,守护进程用 SaveChecked。
func (s Settings) Save(path string) error {
	unlock, err := lockFile(path)
	if err != nil {
		return err
	}
	defer unlock()
	_, err = s.write(path)
	return err
}

// write 落盘,返回写下的字节。调用方持着 lockFile。
func (s Settings) write(path string) ([]byte, error) {
	disk := s
	disk.Rev = 0
	disk.SelectedGroup = ""
	if s.SelectedNode != "" && s.SelectedNode != s.Selected && s.Selected != "" {
		disk.Selected, disk.SelectedGroup = s.SelectedNode, s.Selected
	}
	b, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return nil, err
	}
	// 先落盘再改名:断电时宁可是旧文件,也不能是一份截断的(读不出来就退回默认值,见 Load)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, err
	}
	return b, nil
}

// Preserve 把读不出来的设置文件另存一份(<path>.bad-<时间>),返回另存的路径;文件不存在返回空串。
// Load 失败时拿到的是默认值,第一次保存就会把原文件整份覆盖 —— 订阅、规则组、设备策略全没了。
// 覆盖之前先留一份,用户或开发者还能从里面把东西捞回来。
func Preserve(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	bak := path + ".bad-" + time.Now().Format("20060102-150405")
	return bak, os.WriteFile(bak, b, 0o600)
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
		if !p.Manual && !ValidURL(p.URL) {
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
	if err := s.DefaultRules.validate(); err != nil {
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
	if strings.HasPrefix(value, "[") || strings.HasPrefix(value, "{") { // 规则组、默认规则这类结构化的设置项,值写 JSON
		if err := json.Unmarshal([]byte(value), &v); err != nil {
			return s, fmt.Errorf("%s 的值不是合法的 JSON: %w", key, err)
		}
	} else {
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
// ForPage 给界面(网页面板、Android / 桌面的页面)看的那份:去掉凭据 —— 面板密码哈希(单轮加盐 SHA-256,拿到能离线爆破)、
// 订阅地址(带令牌,拿到就能拉走全部节点的凭据)。页面不用它们:设置页整份保存时服务沿用原来的订阅
// (MSetSettings 不动订阅列表),密码另有接口改;订阅管理要显示、编辑地址,另走带地址的订阅列表(页面方法 GetProfilesWithLinks)。
// 状态每 1.5 秒推一次,局域网明文访问网页面板时路上看得见。
func (s Settings) ForPage() Settings {
	c := s.Clone()
	c.WebPassword, c.ProfileURL = "", ""
	for i := range c.Profiles {
		c.Profiles[i].URL = ""
	}
	return c
}

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
