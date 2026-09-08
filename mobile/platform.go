//go:build linux || android

package mobile

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"strings"
	"sync"
	"unsafe"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/control"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	"github.com/sagernet/sing/common/x/list"
	"golang.org/x/sys/unix"
)

// Host 宿主(Android 的 Kotlin 侧)要实现的几个方法。刻意做得很小:开 TUN、保护套接字、查连接归属、网络接口、默认网络变化。
// 参数与结果尽量用 JSON 字符串,gomobile 绑定简单,两边都好改。
type Host interface {
	// OpenTun 按 tunSpec(JSON)建 VpnService 并返回 TUN 的文件描述符;失败返回负数并把原因写日志
	OpenTun(specJSON string) int32
	// Protect 让一个套接字绕过 VPN(VpnService.protect)
	Protect(fd int32) bool
	// FindConnectionOwner 查一条连接属于哪个 uid(ConnectivityManager.getConnectionOwnerUid);找不到返回 -1
	FindConnectionOwner(ipProtocol int32, sourceAddress string, sourcePort int32, destinationAddress string, destinationPort int32) int32
	// PackageNamesByUid 逗号分隔的包名列表
	PackageNamesByUid(uid int32) string
	// NetworkInterfaces 当前网络接口列表(JSON 数组,见 ifaceSpec)
	NetworkInterfaces() string
	// StartDefaultInterfaceMonitor 开始监听默认网络变化,变化时调 listener.Update
	StartDefaultInterfaceMonitor(listener InterfaceListener) bool
	StopDefaultInterfaceMonitor()
	// Log 内核与引擎日志(级别 + 一行)
	Log(level string, message string)
}

// InterfaceListener 宿主在默认网络变化时回调。index 为 -1 表示没有网络。
type InterfaceListener interface {
	Update(name string, index int32, expensive bool, constrained bool)
}

// tunSpec 传给 OpenTun 的参数。
type tunSpec struct {
	MTU                 int32    `json:"mtu"`
	Inet4Address        []string `json:"inet4Address"`
	Inet6Address        []string `json:"inet6Address"`
	DNS                 []string `json:"dns"`
	AutoRoute           bool     `json:"autoRoute"`
	StrictRoute         bool     `json:"strictRoute"`
	Inet4Routes         []string `json:"inet4Routes"` // auto_route 展开后要加的路由(已排除 route_exclude_address)
	Inet6Routes         []string `json:"inet6Routes"`
	Inet4RouteExcludes  []string `json:"inet4RouteExcludes"`
	Inet6RouteExcludes  []string `json:"inet6RouteExcludes"`
	IncludePackages     []string `json:"includePackages"`
	ExcludePackages     []string `json:"excludePackages"`
	HTTPProxyEnabled    bool     `json:"httpProxyEnabled"`
	HTTPProxyServer     string   `json:"httpProxyServer"`
	HTTPProxyServerPort int32    `json:"httpProxyServerPort"`
}

// ifaceSpec 宿主给的网络接口描述。
type ifaceSpec struct {
	Index     int32    `json:"index"`
	MTU       int32    `json:"mtu"`
	Name      string   `json:"name"`
	Addresses []string `json:"addresses"` // CIDR
	Up        bool     `json:"up"`
	Loopback  bool     `json:"loopback"`
	P2P       bool     `json:"p2p"`
	Multicast bool     `json:"multicast"`
	Type      string   `json:"type"` // wifi / cellular / ethernet / other
	DNS       []string `json:"dns"`
	Gateways  []string `json:"gateways"`
	Metered   bool     `json:"metered"`
}

// platform 实现 sing-box 的 adapter.PlatformInterface,只做 Android 需要的部分,其余明确回答"不支持"。
type platform struct {
	host           Host
	networkManager adapter.NetworkManager
	mu             sync.Mutex
	myTunName      string
	myTunAddress   []netip.Addr
	defaultIface   *control.Interface
	isExpensive    bool
	isConstrained  bool
}

var _ adapter.PlatformInterface = (*platform)(nil)

func newPlatform(h Host) *platform { return &platform{host: h} }

func (p *platform) Initialize(networkManager adapter.NetworkManager) error {
	p.networkManager = networkManager
	return nil
}

func (p *platform) UsePlatformAutoDetectInterfaceControl() bool { return true }
func (p *platform) AutoDetectInterfaceControl(fd int) error {
	if !p.host.Protect(int32(fd)) {
		return E.New("protect socket failed")
	}
	return nil
}

func (p *platform) UsePlatformInterface() bool { return true }

func (p *platform) OpenInterface(options *tun.Options, platformOptions option.TunPlatformOptions) (tun.Tun, error) {
	if len(options.IncludeUID) > 0 || len(options.ExcludeUID) > 0 || len(options.IncludeAndroidUser) > 0 {
		return nil, E.New("platform: unsupported uid / android_user options")
	}
	routeRanges, err := options.BuildAutoRouteRanges(true)
	if err != nil {
		return nil, err
	}
	dns, _ := options.DNSServerAddress()
	spec := tunSpec{
		MTU: int32(options.MTU), AutoRoute: options.AutoRoute, StrictRoute: options.StrictRoute,
		Inet4Address: prefixes(options.Inet4Address), Inet6Address: prefixes(options.Inet6Address),
		DNS:             addrs(dns),
		IncludePackages: options.IncludePackage, ExcludePackages: options.ExcludePackage,
	}
	for _, r := range routeRanges {
		if r.Addr().Is4() {
			spec.Inet4Routes = append(spec.Inet4Routes, r.String())
		} else {
			spec.Inet6Routes = append(spec.Inet6Routes, r.String())
		}
	}
	spec.Inet4RouteExcludes = prefixes(options.Inet4RouteExcludeAddress)
	spec.Inet6RouteExcludes = prefixes(options.Inet6RouteExcludeAddress)
	if platformOptions.HTTPProxy != nil && platformOptions.HTTPProxy.Enabled {
		spec.HTTPProxyEnabled = true
		spec.HTTPProxyServer = platformOptions.HTTPProxy.Server
		spec.HTTPProxyServerPort = int32(platformOptions.HTTPProxy.ServerPort)
	}
	b, _ := json.Marshal(spec)
	fd := p.host.OpenTun(string(b))
	if fd < 0 {
		return nil, E.New("platform: open tun failed")
	}
	name, err := tunName(int(fd))
	if err != nil {
		return nil, E.Cause(err, "query tun name")
	}
	options.Name = name
	if options.InterfaceMonitor != nil {
		options.InterfaceMonitor.RegisterMyInterface(name)
	}
	dupFd, err := unix.Dup(int(fd))
	if err != nil {
		return nil, E.Cause(err, "dup tun file descriptor")
	}
	options.FileDescriptor = dupFd
	p.mu.Lock()
	p.myTunName = name
	p.myTunAddress = p.myTunAddress[:0]
	for _, pre := range options.Inet4Address {
		p.myTunAddress = append(p.myTunAddress, pre.Addr())
	}
	for _, pre := range options.Inet6Address {
		p.myTunAddress = append(p.myTunAddress, pre.Addr())
	}
	p.mu.Unlock()
	return tun.New(*options)
}

func (p *platform) ProcessPlatformOptions(option.TunPlatformOptions) error { return nil }

func (p *platform) MyInterfaceAddress() []netip.Addr {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]netip.Addr(nil), p.myTunAddress...)
}

func (p *platform) UsePlatformDefaultInterfaceMonitor() bool { return true }
func (p *platform) CreateDefaultInterfaceMonitor(logger logger.Logger) tun.DefaultInterfaceMonitor {
	return &defaultMonitor{platform: p, logger: logger}
}

func (p *platform) UsePlatformNetworkInterfaces() bool { return true }
func (p *platform) NetworkInterfaces() ([]adapter.NetworkInterface, error) {
	var specs []ifaceSpec
	if err := json.Unmarshal([]byte(p.host.NetworkInterfaces()), &specs); err != nil {
		return nil, E.Cause(err, "parse interfaces")
	}
	p.mu.Lock()
	def := p.defaultIface
	expensive, constrained := p.isExpensive, p.isConstrained
	tunName := p.myTunName
	p.mu.Unlock()
	seen := map[string]bool{}
	var out []adapter.NetworkInterface
	for _, s := range specs {
		if seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		var flags net.Flags
		if s.Up {
			flags |= net.FlagUp | net.FlagRunning
		}
		if s.Loopback {
			flags |= net.FlagLoopback
		}
		if s.P2P {
			flags |= net.FlagPointToPoint
		}
		if s.Multicast {
			flags |= net.FlagMulticast
		}
		var addresses []netip.Prefix
		for _, a := range s.Addresses {
			if pre, err := netip.ParsePrefix(a); err == nil {
				addresses = append(addresses, pre)
			}
		}
		var gateways []netip.Addr
		for _, g := range s.Gateways {
			if a, err := netip.ParseAddr(g); err == nil {
				gateways = append(gateways, a.Unmap().WithZone(""))
			}
		}
		isDefault := s.Name != tunName && def != nil && int(s.Index) == def.Index
		out = append(out, adapter.NetworkInterface{
			Interface:   control.Interface{Index: int(s.Index), MTU: int(s.MTU), Name: s.Name, Addresses: addresses, Flags: flags},
			Type:        ifaceType(s.Type),
			DNSServers:  s.DNS,
			Gateways:    gateways,
			Expensive:   s.Metered || isDefault && expensive,
			Constrained: isDefault && constrained,
		})
	}
	return out, nil
}

func ifaceType(t string) C.InterfaceType {
	switch strings.ToLower(t) {
	case "wifi":
		return C.InterfaceTypeWIFI
	case "cellular":
		return C.InterfaceTypeCellular
	case "ethernet":
		return C.InterfaceTypeEthernet
	}
	return C.InterfaceTypeOther
}

func (p *platform) UnderNetworkExtension() bool                     { return false }
func (p *platform) NetworkExtensionIncludeAllNetworks() bool        { return false }
func (p *platform) ClearDNSCache()                                  {}
func (p *platform) RequestPermissionForWIFIState() error            { return nil }
func (p *platform) ReadWIFIState(context.Context) adapter.WIFIState { return adapter.WIFIState{} }
func (p *platform) UsePlatformWIFIMonitor() bool                    { return false }

func (p *platform) UsePlatformConnectionOwnerFinder() bool { return true }
func (p *platform) FindConnectionOwner(req *adapter.FindConnectionOwnerRequest) (*adapter.ConnectionOwner, error) {
	uid := p.host.FindConnectionOwner(req.IpProtocol, req.SourceAddress, req.SourcePort, req.DestinationAddress, req.DestinationPort)
	if uid < 0 {
		return nil, E.New("connection owner not found")
	}
	var pkgs []string
	if s := strings.TrimSpace(p.host.PackageNamesByUid(uid)); s != "" {
		pkgs = strings.Split(s, ",")
	}
	return &adapter.ConnectionOwner{UserId: uid, AndroidPackageNames: pkgs}, nil
}

func (p *platform) UsePlatformNotification() bool                { return false }
func (p *platform) SendNotification(*adapter.Notification) error { return E.New("unsupported") }
func (p *platform) CancelNotification(string, int32) error       { return E.New("unsupported") }
func (p *platform) UsePlatformNeighborResolver() bool            { return false }
func (p *platform) StartNeighborMonitor(adapter.NeighborUpdateListener) error {
	return E.New("unsupported")
}
func (p *platform) CloseNeighborMonitor(adapter.NeighborUpdateListener) error {
	return E.New("unsupported")
}
func (p *platform) UsePlatformShell() bool    { return false }
func (p *platform) CheckPlatformShell() error { return E.New("unsupported") }
func (p *platform) OpenShellSession(*adapter.PlatformUser, string, []string, string, int32, int32) (adapter.ShellSession, error) {
	return nil, E.New("unsupported")
}
func (p *platform) LookupUser(string) (*adapter.PlatformUser, error) {
	return nil, E.New("unsupported")
}
func (p *platform) LookupSFTPServer() (string, error)     { return "", E.New("unsupported") }
func (p *platform) ReadSystemSSHHostKey() ([]byte, error) { return nil, E.New("unsupported") }
func (p *platform) TailscaleHostname() string             { return "" }
func (p *platform) UsePlatformBridge() bool               { return false }
func (p *platform) CreateBridge(adapter.BridgeOptions) (adapter.BridgeSession, error) {
	return nil, E.New("unsupported")
}

// ---- 默认网络监听 ----

type defaultMonitor struct {
	*platform
	logger       logger.Logger
	callbacks    list.List[tun.DefaultInterfaceUpdateCallback]
	myInterfaces []string
	initialized  bool
}

var _ tun.DefaultInterfaceMonitor = (*defaultMonitor)(nil)

func (m *defaultMonitor) Start() error {
	if !m.host.StartDefaultInterfaceMonitor(m) {
		return E.New("start default interface monitor failed")
	}
	// 宿主应当在注册时就同步报一次当前网络;万一没报,至少把接口列表拉一遍,内核起步时才有网可用
	m.mu.Lock()
	initialized := m.initialized
	m.mu.Unlock()
	if !initialized && m.networkManager != nil {
		if err := m.networkManager.UpdateInterfaces(); err != nil {
			m.logger.Warn(E.Cause(err, "update interfaces"))
		}
	}
	return nil
}

func (m *defaultMonitor) Close() error {
	m.host.StopDefaultInterfaceMonitor()
	return nil
}

func (m *defaultMonitor) DefaultInterface() *control.Interface {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.defaultIface
}

func (m *defaultMonitor) OverrideAndroidVPN() bool { return false }
func (m *defaultMonitor) AndroidVPNEnabled() bool  { return false }

func (m *defaultMonitor) RegisterCallback(callback tun.DefaultInterfaceUpdateCallback) *list.Element[tun.DefaultInterfaceUpdateCallback] {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callbacks.PushBack(callback)
}

func (m *defaultMonitor) UnregisterCallback(element *list.Element[tun.DefaultInterfaceUpdateCallback]) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callbacks.Remove(element)
}

// Update 宿主回调:默认网络变了。跑在独立 goroutine 里,别占着宿主的线程。
func (m *defaultMonitor) Update(name string, index int32, expensive bool, constrained bool) {
	done := make(chan struct{})
	go func() {
		m.update(name, index, expensive, constrained)
		close(done)
	}()
	<-done
}

func (m *defaultMonitor) update(name string, index int32, expensive bool, constrained bool) {
	m.mu.Lock()
	m.isExpensive, m.isConstrained = expensive, constrained
	m.mu.Unlock()
	if m.networkManager != nil {
		if err := m.networkManager.UpdateInterfaces(); err != nil {
			m.logger.Error(E.Cause(err, "update interfaces"))
		}
	}
	m.mu.Lock()
	if index == -1 {
		m.defaultIface = nil
		m.initialized = true
		callbacks := m.callbacks.Array()
		m.mu.Unlock()
		for _, cb := range callbacks {
			cb(nil, 0)
		}
		return
	}
	old := m.defaultIface
	var newIface *control.Interface
	var err error
	if m.networkManager != nil {
		newIface, err = m.networkManager.InterfaceFinder().ByIndex(int(index))
	} else {
		err = E.New("network manager not ready")
	}
	if err != nil {
		m.mu.Unlock()
		m.logger.Error(E.Cause(err, "find updated interface: ", name))
		return
	}
	m.defaultIface = newIface
	if m.initialized && old != nil && old.Name == newIface.Name && old.Index == newIface.Index {
		m.mu.Unlock()
		return
	}
	m.initialized = true
	callbacks := m.callbacks.Array()
	m.mu.Unlock()
	for _, cb := range callbacks {
		cb(newIface, 0)
	}
}

func (m *defaultMonitor) RegisterMyInterface(interfaceName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.myInterfaces = append(m.myInterfaces, interfaceName)
}

func (m *defaultMonitor) MyInterfaces() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.myInterfaces...)
}

// ---- 小工具 ----

func prefixes(ps []netip.Prefix) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.String())
	}
	return out
}

func addrs(as []netip.Addr) []string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.String())
	}
	return out
}

// tunName 通过 TUNGETIFF 问内核这个 fd 对应的接口名(VpnService 给的 fd 也是 tun 设备)。
func tunName(fd int) (string, error) {
	var ifr [unix.IFNAMSIZ + 64]byte
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(unix.TUNGETIFF), uintptr(unsafe.Pointer(&ifr[0])))
	if errno != 0 {
		return "", E.Cause(errno, "TUNGETIFF")
	}
	return unix.ByteSliceToString(ifr[:]), nil
}
