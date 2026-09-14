//go:build windows

package wfp

import "unsafe"

// permitTunAddress 放行本机地址是隧道地址的连接:只有隧道网卡有这个地址,经隧道出去的流量本机地址就是它;
// 隧道不在的时候系统里没有任何网卡有这个地址,任何连接都匹配不上 —— 天然全拦,也不用跟着网卡 LUID 变。
func permitTunAddress(session uintptr, baseObjects *baseObjects, weight uint8, v4 [4]byte, v6 [16]byte) error {
	m4 := wtFwpV4AddrAndMask{addr: AddrV4(v4), mask: 0xFFFFFFFF}
	c4 := wtFwpmFilterCondition0{
		fieldKey:  cFWPM_CONDITION_IP_LOCAL_ADDRESS,
		matchType: cFWP_MATCH_EQUAL,
		conditionValue: wtFwpConditionValue0{
			_type: cFWP_V4_ADDR_MASK,
			value: uintptr(unsafe.Pointer(&m4)),
		},
	}
	if err := addPermitBothWays(session, baseObjects, weight, &c4, "Permit tunnel address (IPv4)", cFWPM_LAYER_ALE_AUTH_CONNECT_V4, cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4); err != nil {
		return err
	}
	m6 := wtFwpV6AddrAndMask{addr: v6, prefixLength: 128}
	c6 := wtFwpmFilterCondition0{
		fieldKey:  cFWPM_CONDITION_IP_LOCAL_ADDRESS,
		matchType: cFWP_MATCH_EQUAL,
		conditionValue: wtFwpConditionValue0{
			_type: cFWP_V6_ADDR_MASK,
			value: uintptr(unsafe.Pointer(&m6)),
		},
	}
	return addPermitBothWays(session, baseObjects, weight, &c6, "Permit tunnel address (IPv6)", cFWPM_LAYER_ALE_AUTH_CONNECT_V6, cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V6)
}
