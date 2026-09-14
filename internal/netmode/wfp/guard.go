//go:build windows

// Package wfp 在 Windows 过滤平台(WFP)里放一组过滤器,做「全局禁直连」的闸。
//
// 这个包改自 wireguard-windows 的 tunnel/firewall(MIT,Copyright (C) 2019-2021 WireGuard LLC,
// 许可证全文见仓库 NOTICE):过滤器、系统调用与类型定义照搬,去掉了 DNS 限制与 Hyper-V 那两块,
// 加了按局域网放行,隧道网卡的放行改成可以在会话里替换(网卡重建后 LUID 可能变)。
//
// 会话是动态的(FWPM_SESSION_FLAG_DYNAMIC):进程退出、句柄关闭,所有过滤器自动消失 ——
// 服务被强杀也不会把机器留在断网状态。
package wfp

import (
	"errors"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

type wfpObjectInstaller func(uintptr) error

// baseObjects 本会话注册的提供者与子层。
type baseObjects struct {
	provider windows.GUID
	filters  windows.GUID
}

var (
	mu         sync.Mutex
	session    uintptr
	base       *baseObjects
	tunFilters []uint64 // 当前放行隧道网卡的那几条过滤器的 id,换 LUID 时删掉重加
)

func createWfpSession() (uintptr, error) {
	sessionDisplayData, err := createWtFwpmDisplayData0("godusevpn", "godusevpn dynamic session")
	if err != nil {
		return 0, wrapErr(err)
	}
	s := wtFwpmSession0{
		displayData:          *sessionDisplayData,
		flags:                cFWPM_SESSION_FLAG_DYNAMIC,
		txnWaitTimeoutInMSec: windows.INFINITE,
	}
	sessionHandle := uintptr(0)
	if err := fwpmEngineOpen0(nil, cRPC_C_AUTHN_WINNT, nil, &s, unsafe.Pointer(&sessionHandle)); err != nil {
		return 0, wrapErr(err)
	}
	return sessionHandle, nil
}

func registerBaseObjects(session uintptr) (*baseObjects, error) {
	bo := &baseObjects{}
	var err error
	if bo.provider, err = windows.GenerateGUID(); err != nil {
		return nil, wrapErr(err)
	}
	if bo.filters, err = windows.GenerateGUID(); err != nil {
		return nil, wrapErr(err)
	}
	{
		displayData, err := createWtFwpmDisplayData0("godusevpn", "godusevpn provider")
		if err != nil {
			return nil, wrapErr(err)
		}
		provider := wtFwpmProvider0{providerKey: bo.provider, displayData: *displayData}
		if err := fwpmProviderAdd0(session, &provider, 0); err != nil {
			return nil, wrapErr(err)
		}
	}
	{
		displayData, err := createWtFwpmDisplayData0("godusevpn filters", "Permissive and blocking filters")
		if err != nil {
			return nil, wrapErr(err)
		}
		sublayer := wtFwpmSublayer0{
			subLayerKey: bo.filters,
			displayData: *displayData,
			providerKey: &bo.provider,
			weight:      ^uint16(0), // 子层权重最高:别的防火墙产品的放行规则盖不过我们的拦截
		}
		if err := fwpmSubLayerAdd0(session, &sublayer, 0); err != nil {
			return nil, wrapErr(err)
		}
	}
	return bo, nil
}

// Enable 开闸:放行本进程、回环、(可选)局域网、DHCP、邻居发现,其余全拦。
// 隧道网卡这时候多半还没起来,它的放行由 SetTunLUID 补上。已经开着就什么都不做。
func Enable(lan bool) error {
	mu.Lock()
	defer mu.Unlock()
	if session != 0 {
		return nil
	}
	s, err := createWfpSession()
	if err != nil {
		return err
	}
	var bo *baseObjects
	err = runTransaction(s, func(s uintptr) error {
		var err error
		if bo, err = registerBaseObjects(s); err != nil {
			return err
		}
		if err := permitSelf(s, bo, 15); err != nil {
			return err
		}
		if err := permitLoopback(s, bo, 13); err != nil {
			return err
		}
		if lan {
			if err := permitLAN(s, bo, 12); err != nil {
				return err
			}
		}
		if err := permitDHCPIPv4(s, bo, 12); err != nil {
			return err
		}
		if err := permitDHCPIPv6(s, bo, 12); err != nil {
			return err
		}
		if err := permitNdp(s, bo, 12); err != nil {
			return err
		}
		return blockAll(s, bo, 0)
	})
	if err != nil {
		fwpmEngineClose0(s)
		return err
	}
	session, base, tunFilters = s, bo, nil
	return nil
}

// SetTunLUID 放行经这张隧道网卡的流量;在同一个事务里加新的、删旧的,中间没有既不放行也没拦下的空档。
func SetTunLUID(luid uint64) error {
	mu.Lock()
	defer mu.Unlock()
	if session == 0 {
		return errors.New("闸没开")
	}
	var ids []uint64
	err := runTransaction(session, func(s uintptr) error {
		var err error
		if ids, err = permitTunInterface(s, base, 12, luid); err != nil {
			return err
		}
		for _, old := range tunFilters {
			if err := fwpmFilterDeleteById0(s, old); err != nil {
				return wrapErr(err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	tunFilters = ids
	return nil
}

// Enabled 闸开着没有。
func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return session != 0
}

// Disable 撤闸:关掉会话,所有过滤器随之消失。
func Disable() {
	mu.Lock()
	defer mu.Unlock()
	if session != 0 {
		fwpmEngineClose0(session)
		session, base, tunFilters = 0, nil, nil
	}
}
