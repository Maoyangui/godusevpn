//go:build windows

package wfp

import (
	"runtime"
	"unsafe"
)

// permitTunAddress 放行本机地址是隧道地址的连接:只有隧道网卡有这个地址,经隧道出去的流量本机地址就是它;
// 隧道不在的时候系统里没有任何网卡有这个地址,任何连接都匹配不上 —— 天然全拦,也不用跟着网卡 LUID 变。
//
// 条件值(地址掩码)以 uintptr 挂在 FWP_CONDITION_VALUE0 里交给 DLL,编译器和垃圾回收都看不见这个引用。以前是
// 局部变量,留在协程栈上;栈在交给 DLL 之前一搬家,DLL 读到的就是旧地址上的垃圾。用 Pinner 钉住:交给 Pin
// 会让它分配在堆上(堆对象不搬家),并且在 Unpin 之前不会被回收。
func permitTunAddress(session uintptr, baseObjects *baseObjects, weight uint8, v4 [4]byte, v6 [16]byte) error {
	var pin runtime.Pinner
	defer pin.Unpin()
	m4 := &wtFwpV4AddrAndMask{addr: AddrV4(v4), mask: 0xFFFFFFFF}
	pin.Pin(m4)
	c4 := wtFwpmFilterCondition0{
		fieldKey:  cFWPM_CONDITION_IP_LOCAL_ADDRESS,
		matchType: cFWP_MATCH_EQUAL,
		conditionValue: wtFwpConditionValue0{
			_type: cFWP_V4_ADDR_MASK,
			value: uintptr(unsafe.Pointer(m4)),
		},
	}
	if err := addPermitBothWays(session, baseObjects, weight, &c4, "Permit tunnel address (IPv4)", cFWPM_LAYER_ALE_AUTH_CONNECT_V4, cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4); err != nil {
		return err
	}
	m6 := &wtFwpV6AddrAndMask{addr: v6, prefixLength: 128}
	pin.Pin(m6)
	c6 := wtFwpmFilterCondition0{
		fieldKey:  cFWPM_CONDITION_IP_LOCAL_ADDRESS,
		matchType: cFWP_MATCH_EQUAL,
		conditionValue: wtFwpConditionValue0{
			_type: cFWP_V6_ADDR_MASK,
			value: uintptr(unsafe.Pointer(m6)),
		},
	}
	return addPermitBothWays(session, baseObjects, weight, &c6, "Permit tunnel address (IPv6)", cFWPM_LAYER_ALE_AUTH_CONNECT_V6, cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V6)
}
