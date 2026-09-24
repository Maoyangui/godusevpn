//go:build windows

// Package wfp 在 Windows 过滤平台(WFP)里放一组过滤器,做「全局禁直连」的闸。
//
// 这个包改自 wireguard-windows 的 tunnel/firewall(MIT,Copyright (C) 2019-2021 WireGuard LLC,
// 许可证全文见仓库 NOTICE):过滤器、系统调用与类型定义照搬,去掉了 DNS 限制与 Hyper-V 那两块,
// 加了按局域网放行、按隧道地址放行,以及把对象改成持久的。
//
// 闸是持久的:提供者、子层、过滤器都带 PERSISTENT 标志,写进 BFE 的持久存储 —— 进程退出、被强杀、
// 崩溃、升级换文件、机器重启,闸都还在;另有一组 BOOTTIME 过滤器,从内核网络初始化到 BFE 启动之间
// 那几秒也堵住。只有明确撤闸(用户点断开、切模式、关开关、卸载)才删。所有对象挂在固定 GUID 的
// 提供者下,任何一个进程(服务、恢复命令、卸载程序)都能按它找到并删掉。
package wfp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type wfpObjectInstaller func(uintptr) error

// baseObjects 我们的提供者与子层。
type baseObjects struct {
	provider windows.GUID
	filters  windows.GUID
}

// 固定 GUID:换进程、换版本都不变,恢复命令与卸载程序靠它找对象。
//
// 这是**第二代**(0.7.4 起)。第一代(…2c / …2d)在 m29 那几版(0.6.25-m29 ~ 0.7.1)被绑上了服务名
// (为什么不绑见 baseProvider),而 WFP 的提供者建好就改不了,只能删了重建;删提供者得先删掉引用它的子层,
// 删子层得先删光子层里的过滤器 —— 全按同一个 GUID 做,中间必然有一段没有闸,以前只能等用户下次
// 断开时才换。换一代 GUID 就没有这个问题:建新提供者、新子层、装新过滤器、删旧过滤器放在**同一个事务**里,
// BFE 原子地提交;旧子层与旧提供者此时已没人引用,随后再收掉(dropLegacyBase)。两代的过滤器 ourFilters 都认。
var (
	providerKey = windows.GUID{Data1: 0x6f6d9e2e, Data2: 0x3a41, Data3: 0x4b8e, Data4: [8]byte{0x9d, 0x55, 0x67, 0x6f, 0x64, 0x75, 0x73, 0x65}}
	sublayerKey = windows.GUID{Data1: 0x6f6d9e2f, Data2: 0x3a41, Data3: 0x4b8e, Data4: [8]byte{0x9d, 0x55, 0x67, 0x6f, 0x64, 0x75, 0x73, 0x65}}
	base        = &baseObjects{provider: providerKey, filters: sublayerKey}

	// 第一代的 GUID:只用来认出并收掉旧对象,绝不再往它名下装任何东西。
	legacyProviderKey = windows.GUID{Data1: 0x6f6d9e2c, Data2: 0x3a41, Data3: 0x4b8e, Data4: [8]byte{0x9d, 0x55, 0x67, 0x6f, 0x64, 0x75, 0x73, 0x65}}
	legacySublayerKey = windows.GUID{Data1: 0x6f6d9e2d, Data2: 0x3a41, Data3: 0x4b8e, Data4: [8]byte{0x9d, 0x55, 0x67, 0x6f, 0x64, 0x75, 0x73, 0x65}}
)

// isOurs 一条过滤器是不是我们的:挂在两代任一子层下、或提供者是两代任一。
// 少认一代就会漏删:升级后旧一代的过滤器永远留在系统里,撤闸 / 卸载之后直连仍然被拦。
func isOurs(subLayer windows.GUID, provider *windows.GUID) bool {
	if subLayer == sublayerKey || subLayer == legacySublayerKey {
		return true
	}
	return provider != nil && (*provider == providerKey || *provider == legacyProviderKey)
}

// Spec 闸的参数。
type Spec struct {
	LAN  bool     // 放行局域网 / 私网
	Tun4 [4]byte  // 隧道 v4 地址;经隧道出去的连接本机地址就是它,按它放行
	Tun6 [16]byte // 隧道 v6 地址
	// SelfPath 放行的进程:空 = 当前进程。真机测试时测试进程给现役服务开闸,就填服务 exe 的路径
	SelfPath string
}

const (
	fwpProviderFlagPersistent = 0x00000001 // FWPM_PROVIDER_FLAG_PERSISTENT

	fwpEFilterNotFound   = syscall.Errno(0x80320003)
	fwpEProviderNotFound = syscall.Errno(0x80320005)
	fwpESublayerNotFound = syscall.Errno(0x80320007)
	fwpENotFound         = syscall.Errno(0x80320008)
	fwpEAlreadyExists    = syscall.Errno(0x80320009)
	fwpEInUse            = syscall.Errno(0x80320010) // 被别的对象引用,删不掉
)

var (
	mu       sync.Mutex
	curFlags wtFwpmFilterFlags // 正在装的这一批过滤器的标志(持久 / 开机),rules.go 里每条过滤器都带上
)

func notFound(err error) bool {
	return errors.Is(err, fwpEFilterNotFound) || errors.Is(err, fwpEProviderNotFound) || errors.Is(err, fwpESublayerNotFound) || errors.Is(err, fwpENotFound)
}

// inUse 是不是"被引用、删不掉"。
func inUse(err error) bool { return errors.Is(err, fwpEInUse) }

// openSession 开一个非动态会话:里面加的对象不随会话关闭而消失。
func openSession() (uintptr, error) {
	dd, err := createWtFwpmDisplayData0("godusevpn", "godusevpn guard session")
	if err != nil {
		return 0, wrapErr(err)
	}
	s := wtFwpmSession0{displayData: *dd, txnWaitTimeoutInMSec: windows.INFINITE}
	var h uintptr
	if err := fwpmEngineOpen0(nil, cRPC_C_AUTHN_WINNT, nil, &s, unsafe.Pointer(&h)); err != nil {
		return 0, wrapErr(err)
	}
	return h, nil
}

// baseProvider 造我们的 WFP 提供者。
//
// **不给它填 serviceName。**
//
// FWPM_PROVIDER0.serviceName 的真实语义(微软文档原文 + 真机实测,2026-09-24):**在 BFE 启动时**,
// 挂了服务名、而那个服务不是"自动启动"的提供者,它名下的过滤器被停用(提供者带上 FWPM_PROVIDER_FLAG_DISABLED)。
// 服务**停止**本身不影响:CI 上装 0.7.1(挂着服务名)、连上严格全局、sc stop 之后 5 秒 / 25 秒,
// 0 条过滤器被停用、绑物理网卡的直连全拦。用户那台 Win10 19045 在 0.7.2(不挂服务名)下带着闸真实重启过一次,
// 重启后我们的提供者没有 DISABLED 标志;同机微软挂在 IKEEXT / PolicyAgent(非自动启动)上的提供者则带着。
// 文档里"没挂服务名也会被停用"那半句,真机上没有复现。
//
// 所以绑上服务名的代价是:闸能不能跨过开机,取决于 godusevpn 服务的启动类型。管理员(或别的软件)把它改成
// 手动 / 禁用、或者服务对象被删而闸没撤,下次开机 BFE 一起来闸就失效,而且没人会发现。不绑就没有这层依赖。
// 0.7.2 去掉绑定时写的理由("服务一停闸就失效、普通用户 sc stop 就能关闸")是错的,已被上面的实测否定;
// 结论(不绑)不变。m29 的 f183df1 绑过一次,这里钉住:serviceName 必须是 nil。
func baseProvider(dd *wtFwpmDisplayData0) wtFwpmProvider0 {
	return wtFwpmProvider0{providerKey: providerKey, displayData: *dd, flags: fwpProviderFlagPersistent}
}

// dropLegacyBase 收掉第一代的子层与提供者。只在第一代的过滤器已经删光之后调(Enable 的持久事务提交后、
// Disable 删完过滤器后),否则 BFE 会按"还被引用"拒绝。它们名下已经没有任何过滤器,留着不影响闸,
// 删不掉也只是收尾没做完:返回警告,绝不能让开闸因为收拾旧账而失败,否则用户只能去关「全局禁直连」。
//
// 第一代提供者在 m29 那几版被绑上了服务名(闸能不能跨过开机就取决于服务的启动类型,见 baseProvider)。
// 0.7.2 / 0.7.3 只能等用户下次断开时才换掉;现在 Enable 在一个事务里把过滤器换到第二代名下,
// 这里只剩收尾,换提供者这一步没有空窗。(0.6.13-m15 起停旧服务不会让旧过滤器失效,实测见 baseProvider;
// 更早的 0.6.10-m12 ~ 0.6.12-m14 三个预发布版的闸是动态会话 / 停服务时自己撤,从它们直接升级有一段没闸,见排障页。
// 0.7.4 的安装器曾在停旧服务之前先用新版 exe 预装第二代;0.7.5 去掉了:升级一旦在那之后中止,
// 留下的旧版认不出第二代,断开 / 恢复网络 / 卸载都撤不掉它,反而会把人卡在断网里。)
func dropLegacyBase(s uintptr) string {
	if err := runTransaction(s, func(s uintptr) error {
		if err := fwpmSubLayerDeleteByKey0(s, &legacySublayerKey); err != nil && !notFound(err) {
			return wrapErr(err)
		}
		if err := fwpmProviderDeleteByKey0(s, &legacyProviderKey); err != nil && !notFound(err) {
			return wrapErr(err)
		}
		return nil
	}); err != nil {
		return "旧一代的子层 / 提供者没收掉(闸不受影响,下次开闸再试): " + err.Error()
	}
	return ""
}

// ensureBase 提供者与子层:没有就建(持久),已有就沿用。
func ensureBase(session uintptr) error {
	dd, err := createWtFwpmDisplayData0("godusevpn", "godusevpn provider")
	if err != nil {
		return wrapErr(err)
	}
	provider := baseProvider(dd)
	if err := fwpmProviderAdd0(session, &provider, 0); err != nil && !errors.Is(err, fwpEAlreadyExists) {
		return wrapErr(err)
	}
	dd2, err := createWtFwpmDisplayData0("godusevpn filters", "Permissive and blocking filters")
	if err != nil {
		return wrapErr(err)
	}
	sub := wtFwpmSublayer0{
		subLayerKey: sublayerKey, displayData: *dd2, flags: cFWPM_SUBLAYER_FLAG_PERSISTENT,
		providerKey: &providerKey,
		weight:      ^uint16(0), // 子层权重最高:别的防火墙产品的放行规则盖不过我们的拦截
	}
	if err := fwpmSubLayerAdd0(session, &sub, 0); err != nil && !errors.Is(err, fwpEAlreadyExists) {
		return wrapErr(err)
	}
	return nil
}

// installSet 装一组过滤器。withSelf 是持久那组(放行本进程);开机那组没有进程可放行,
// ALE_APP_ID 条件在 BFE 起来之前也不受支持,所以不带。
func installSet(session uintptr, spec Spec, withSelf, withForward bool) error {
	if withSelf {
		if err := permitSelf(session, base, 15, spec.SelfPath); err != nil {
			return err
		}
	}
	if err := permitLoopback(session, base, 13); err != nil {
		return err
	}
	if err := permitTunAddress(session, base, 12, spec.Tun4, spec.Tun6); err != nil {
		return err
	}
	if spec.LAN {
		if err := permitLAN(session, base, 12); err != nil {
			return err
		}
	}
	if err := permitDHCPIPv4(session, base, 12); err != nil {
		return err
	}
	if err := permitDHCPIPv6(session, base, 12); err != nil {
		return err
	}
	if err := permitNdp(session, base, 12); err != nil {
		return err
	}
	if withForward {
		if err := installForward(session, spec); err != nil {
			return err
		}
	}
	return blockAll(session, base, 0)
}

// installForward 转发层(热点共享 / ICS)那组:默认全拦;局域网直通开着就放行去往私网的转发;
// 经隧道的那组在 TunUp 里按接口号装。单独一个函数:开机那组把它放在自己的事务里装,
// BFE 万一不收转发层的开机过滤器,也不能把四个 ALE 层的开机保护一起拖没。
func installForward(session uintptr, spec Spec) error {
	if spec.LAN {
		if err := permitForwardLAN(session, base, 12); err != nil {
			return err
		}
	}
	return blockForward(session, base, 0)
}

// Enable 开闸(幂等)。
//
// 运行期那组(PERSISTENT)是闸本身:删旧的与装新的必须在同一个事务里,失败就回滚、旧闸原样留着,
// 绝不留下半套保护 —— 这一组装不上就是真的没保护,报错是对的。
//
// 开机那两组(BOOTTIME)只覆盖"内核网络起来到 BFE 启动"那几秒,装不上不等于此刻在漏。
// 把它们并进同一个事务、失败即整体回滚(m29 的 f183df1 就是这么改的),等于让一个只影响开机
// 几秒的问题把用户整个挡在门外 —— 而用户挡不住就会去关掉「全局禁直连」,反倒更不私密。
// 所以各自一个事务,失败记成警告,由界面与日志如实报出来。
func Enable(spec Spec) (warn string, err error) {
	mu.Lock()
	defer mu.Unlock()
	s, err := openSession()
	if err != nil {
		return "", err
	}
	defer fwpmEngineClose0(s)
	if err = runTransaction(s, func(s uintptr) error {
		if err := ensureBase(s); err != nil {
			return err
		}
		// 两代的过滤器一起删:从 m29 那几版升上来时,旧一代(绑了服务名)的过滤器和新一代的在同一个
		// 事务里换掉,BFE 原子提交 —— 换提供者这一步没有不设防的窗口。
		if err := deleteOurFilters(s); err != nil {
			return err
		}
		curFlags = cFWPM_FILTER_FLAG_PERSISTENT
		return installSet(s, spec, true, true)
	}); err != nil {
		return "", fmt.Errorf("全局禁直连过滤器事务回滚,保留旧保护: %w", err)
	}
	legacyWarn := dropLegacyBase(s)
	if err := runTransaction(s, func(s uintptr) error {
		curFlags = cFWPM_FILTER_FLAG_BOOTTIME
		return installSet(s, spec, false, false)
	}); err != nil {
		warn = fmt.Sprintf("开机那组过滤器没装上(开机到 BFE 启动之间那几秒不受保护): %v", err)
	}
	// 转发层的开机过滤器另起一个事务:它装不上只影响开机那几秒经本机转发的流量(热点共享),
	// 别连累上面那组。
	if err := runTransaction(s, func(s uintptr) error {
		curFlags = cFWPM_FILTER_FLAG_BOOTTIME
		return installForward(s, spec)
	}); err != nil {
		warn = joinWarn(warn, fmt.Sprintf("开机转发那组过滤器没装上(开机那几秒经本机转发的流量不受保护): %v", err))
	}
	return joinWarn(warn, legacyWarn), nil
}

func joinWarn(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + ";" + b
	}
}

// BootGuardReady verifies that at least one enabled BOOTTIME filter owned by
// this product is present.  The check is intentionally made against BFE's
// persisted objects rather than process memory so a restarted daemon cannot
// mistake a stale in-memory flag for boot protection.
func BootGuardReady() (bool, error) {
	mu.Lock()
	defer mu.Unlock()
	s, err := openSession()
	if err != nil {
		return false, err
	}
	defer fwpmEngineClose0(s)
	fs, err := ourFilters(s)
	if err != nil {
		return false, err
	}
	// A single surviving boot filter is not sufficient: the guard covers
	// outbound/connect, inbound/accept, and forwarding on both address
	// families.  Require every layer to have an enabled BOOTTIME filter so a
	// partially corrupted persistent store cannot be mistaken for protection.
	return bootGuardCovers(fs), nil
}

func bootGuardCovers(fs []filterInfo) bool {
	covered := make(map[windows.GUID]bool, len(ourLayers()))
	for _, f := range fs {
		if f.flags&cFWPM_FILTER_FLAG_BOOTTIME != 0 && f.flags&cFWPM_FILTER_FLAG_DISABLED == 0 && f.action == cFWP_ACTION_BLOCK {
			covered[f.layer] = true
		}
	}
	for _, layer := range ourLayers() {
		if !covered[layer] {
			return false
		}
	}
	return len(covered) == len(ourLayers())
}

// Disable 撤闸:删过滤器、子层、提供者。不存在也不算错。
//
// 分两个事务:先把过滤器删干净并提交,再删子层与提供者。放在同一个事务里时,BFE 删子层会报
// "被其他对象引用"(它按提交前的状态查引用),整个事务回滚,过滤器一条也删不掉。
// 过滤器删完就已经"没闸"了;子层/提供者要是删不掉,只当收尾没做完,不影响联网。
func Disable() error {
	mu.Lock()
	defer mu.Unlock()
	s, err := openSession()
	if err != nil {
		return err
	}
	defer fwpmEngineClose0(s)
	if err := runTransaction(s, deleteOurFilters); err != nil {
		return err
	}
	left, err := ourFilterKeys(s)
	if err != nil {
		return err
	}
	if len(left) != 0 {
		return fmt.Errorf("撤闸后仍有 %d 条过滤器", len(left))
	}
	if err := runTransaction(s, func(s uintptr) error {
		// 两代的子层与提供者都收掉(旧一代在升级后可能还留着空壳)。子层引用提供者,先删子层。
		for _, k := range []*windows.GUID{&sublayerKey, &legacySublayerKey} {
			if err := fwpmSubLayerDeleteByKey0(s, k); err != nil && !notFound(err) {
				if inUse(err) {
					return fmt.Errorf("过滤器已全部删除(闸已撤),但子层还被别的对象引用,删不掉: %w", err)
				}
				return wrapErr(err)
			}
		}
		for _, k := range []*windows.GUID{&providerKey, &legacyProviderKey} {
			if err := fwpmProviderDeleteByKey0(s, k); err != nil && !notFound(err) {
				if inUse(err) {
					return fmt.Errorf("过滤器已全部删除(闸已撤),但提供者还被别的对象引用,删不掉: %w", err)
				}
				return wrapErr(err)
			}
		}
		return nil
	}); err != nil {
		return &CleanupError{Err: err}
	}
	return nil
}

// CleanupError 撤闸时过滤器已经全部删掉(闸已撤、联网正常),只是子层 / 提供者的收尾没做完。
// 调用方应当作警告而不是"闸还在":记成撤闸失败的话界面会一直说直连被拦,而实际上一条过滤器都没有。
type CleanupError struct{ Err error }

func (e *CleanupError) Error() string { return e.Err.Error() }
func (e *CleanupError) Unwrap() error { return e.Err }

// Count 我们的过滤器数量(含开机那组);0 = 闸没开。
func Count() (int, error) {
	mu.Lock()
	defer mu.Unlock()
	s, err := openSession()
	if err != nil {
		return 0, err
	}
	defer fwpmEngineClose0(s)
	keys, err := ourFilterKeys(s)
	if err != nil {
		return 0, err
	}
	return len(keys), nil
}

func guardCovers(fs []filterInfo, required wtFwpmFilterFlags) bool {
	covered := make(map[windows.GUID]bool, len(ourLayers()))
	for _, f := range fs {
		if f.flags&required != 0 && f.flags&cFWPM_FILTER_FLAG_DISABLED == 0 && f.action == cFWP_ACTION_BLOCK {
			covered[f.layer] = true
		}
	}
	for _, layer := range ourLayers() {
		if !covered[layer] {
			return false
		}
	}
	return len(covered) == len(ourLayers())
}

// PersistentGuardReady 核查的是**运行期**那组(PERSISTENT):它要覆盖我们保护的每一层。
// 这一组是闸本身 —— 它在,进程死了、崩了、换文件了、重启了,闸都还在;它不在就是真的没保护,
// 所以守护进程拿它当启动前的硬条件是合理的、而且在 Windows 上一定满足得了。
//
// 开机那组(BOOTTIME)**不**在这里要求:它只覆盖开机到 BFE 启动之间那几秒,装不上不代表此刻在漏。
// m29 曾把它也并进来,于是"开机组少一条"就等于 Windows 上全局模式完全连不上。它的状态由
// BootGuardReady 单独报,并经 GuardWarning 如实告诉用户。
// Count 仍然是给诊断和收尾用的原始条数。
func PersistentGuardReady() (bool, error) {
	fs, err := currentFilters()
	if err != nil {
		return false, err
	}
	return persistentGuardReady(fs), nil
}

// persistentGuardReady 拆出来只为可测:真机上没法造一组过滤器,但这条判据正是"Windows 上能不能连"
// 的开关,必须钉住 —— 上一版就是只改了这里的注释、函数体原样留着 AND BOOTTIME,等于什么都没改。
func persistentGuardReady(fs []filterInfo) bool {
	return guardCovers(fs, cFWPM_FILTER_FLAG_PERSISTENT)
}

// Breakdown 只读诊断:我们名下的过滤器一共几条、其中几条被 BFE 标成 DISABLED(不拦任何包)、
// 运行期那组(PERSISTENT)覆盖全不全。给 guard status 与真机验收用,不参与任何决策。
func Breakdown() (total, disabled int, persistentReady bool, err error) {
	fs, err := currentFilters()
	if err != nil {
		return 0, 0, false, err
	}
	for _, f := range fs {
		if f.flags&cFWPM_FILTER_FLAG_DISABLED != 0 {
			disabled++
		}
	}
	return len(fs), disabled, persistentGuardReady(fs), nil
}

// ProviderBootDisabled 两代提供者里有没有哪个带着 FWPM_PROVIDER_FLAG_DISABLED —— 也就是上次开机时
// BFE 把它名下的过滤器停用过。只读诊断:守护进程启动时记一笔,guard status 也显示。
// 用户那台 Win10 上带着闸真实重启过一次,不挂服务名的提供者没被停用;这条留着,每次开机都能自证一次。
func ProviderBootDisabled() (bool, error) {
	mu.Lock()
	defer mu.Unlock()
	s, err := openSession()
	if err != nil {
		return false, err
	}
	defer fwpmEngineClose0(s)
	for _, k := range []*windows.GUID{&providerKey, &legacyProviderKey} {
		if fl, ok := providerFlags(s, k); ok && fl&fwpProviderFlagDisabled != 0 {
			return true, nil
		}
	}
	return false, nil
}

// currentFilters 开一次会话把我们名下的过滤器全取出来。
func currentFilters() ([]filterInfo, error) {
	mu.Lock()
	defer mu.Unlock()
	s, err := openSession()
	if err != nil {
		return nil, err
	}
	defer fwpmEngineClose0(s)
	return ourFilters(s)
}

// deleteOurFilters 删掉提供者名下的全部过滤器(四个 ALE 层,含开机那组)。
func deleteOurFilters(session uintptr) error {
	keys, err := ourFilterKeys(session)
	if err != nil {
		return err
	}
	for i := range keys {
		if err := fwpmFilterDeleteByKey0(session, &keys[i]); err != nil && !notFound(err) {
			return wrapErr(err)
		}
	}
	return nil
}

// AddrV4 把点分地址转成 WFP 要的主机字节序整数。
func AddrV4(a [4]byte) uint32 { return binary.BigEndian.Uint32(a[:]) }
