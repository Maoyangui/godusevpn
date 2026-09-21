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

const providerServiceName = "godusevpn"

// 固定 GUID:换进程、换版本都不变,恢复命令与卸载程序靠它找对象。
var (
	providerKey = windows.GUID{Data1: 0x6f6d9e2c, Data2: 0x3a41, Data3: 0x4b8e, Data4: [8]byte{0x9d, 0x55, 0x67, 0x6f, 0x64, 0x75, 0x73, 0x65}}
	sublayerKey = windows.GUID{Data1: 0x6f6d9e2d, Data2: 0x3a41, Data3: 0x4b8e, Data4: [8]byte{0x9d, 0x55, 0x67, 0x6f, 0x64, 0x75, 0x73, 0x65}}
	base        = &baseObjects{provider: providerKey, filters: sublayerKey}
)

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

// ensureBase 提供者与子层:没有就建(持久),已有就沿用。
func ensureBase(session uintptr) error {
	dd, err := createWtFwpmDisplayData0("godusevpn", "godusevpn provider")
	if err != nil {
		return wrapErr(err)
	}
	provider := wtFwpmProvider0{providerKey: providerKey, displayData: *dd, flags: fwpProviderFlagPersistent}
	serviceName, err := windows.UTF16PtrFromString(providerServiceName)
	if err != nil {
		return err
	}
	provider.serviceName = serviceName
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

// Enable 开闸(幂等):删除旧过滤器并安装持久/开机/转发三组过滤器必须在同一
// WFP 事务中完成。任一组失败都会回滚，旧闸继续存在，绝不留下半套保护。
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
		if err := deleteOurFilters(s); err != nil {
			return err
		}
		curFlags = cFWPM_FILTER_FLAG_PERSISTENT
		if err := installSet(s, spec, true, true); err != nil {
			return err
		}
		curFlags = cFWPM_FILTER_FLAG_BOOTTIME
		if err := installSet(s, spec, false, false); err != nil {
			return fmt.Errorf("开机保护过滤器安装失败: %w", err)
		}
		if err := installForward(s, spec); err != nil {
			return fmt.Errorf("开机转发保护过滤器安装失败: %w", err)
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("全局禁直连过滤器事务回滚,保留旧保护: %w", err)
	}
	return warn, nil
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
		if err := fwpmSubLayerDeleteByKey0(s, &sublayerKey); err != nil && !notFound(err) {
			if inUse(err) {
				return fmt.Errorf("过滤器已全部删除(闸已撤),但子层还被别的对象引用,删不掉: %w", err)
			}
			return wrapErr(err)
		}
		if err := fwpmProviderDeleteByKey0(s, &providerKey); err != nil && !notFound(err) {
			if inUse(err) {
				return fmt.Errorf("过滤器已全部删除(闸已撤),但提供者还被别的对象引用,删不掉: %w", err)
			}
			return wrapErr(err)
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
