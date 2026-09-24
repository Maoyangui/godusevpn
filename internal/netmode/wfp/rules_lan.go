//go:build windows

package wfp

import "unsafe"

// lanV4 / lanV6 局域网 / 私网 / 本地多播的地址段(permitLAN 与 permitForwardLAN 共用)。
// 必须是包级变量:条件值以 uintptr 挂在 FWP_CONDITION_VALUE0 里交给 DLL,编译器和垃圾回收都看不见这个引用。
// 以前是函数里的局部切片,可能留在协程栈上;栈在交给 DLL 之前一搬家,DLL 读到的就是旧地址上的垃圾 ——
// 闸的放行范围可能被读错。包级变量在静态内存里,不会搬家。地址掩码按 FWP_V4_ADDR_AND_MASK /
// FWP_V6_ADDR_AND_MASK 给,v4 是主机字节序。
var lanV4 = [...]wtFwpV4AddrAndMask{
	{addr: 0x0A000000, mask: 0xFF000000}, // 10.0.0.0/8
	{addr: 0xAC100000, mask: 0xFFF00000}, // 172.16.0.0/12
	{addr: 0xC0A80000, mask: 0xFFFF0000}, // 192.168.0.0/16
	{addr: 0xA9FE0000, mask: 0xFFFF0000}, // 169.254.0.0/16
	{addr: 0xE0000000, mask: 0xF0000000}, // 224.0.0.0/4
	{addr: 0xFFFFFFFF, mask: 0xFFFFFFFF}, // 255.255.255.255
}

var lanV6 = [...]wtFwpV6AddrAndMask{
	{addr: [16]uint8{0xfe, 0x80}, prefixLength: 10}, // fe80::/10
	{addr: [16]uint8{0xff}, prefixLength: 8},        // ff00::/8
	{addr: [16]uint8{0xfc}, prefixLength: 7},        // fc00::/7
}

// permitLAN 放行去往局域网 / 私网 / 本地多播的流量:这些不出网,不算漏。
func permitLAN(session uintptr, baseObjects *baseObjects, weight uint8) error {
	v4, v6 := &lanV4, &lanV6
	for i := range v4 {
		condition := wtFwpmFilterCondition0{
			fieldKey:  cFWPM_CONDITION_IP_REMOTE_ADDRESS,
			matchType: cFWP_MATCH_EQUAL,
			conditionValue: wtFwpConditionValue0{
				_type: cFWP_V4_ADDR_MASK,
				value: uintptr(unsafe.Pointer(&v4[i])),
			},
		}
		if err := addPermitBothWays(session, baseObjects, weight, &condition, "Permit LAN (IPv4)", cFWPM_LAYER_ALE_AUTH_CONNECT_V4, cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4); err != nil {
			return err
		}
	}
	for i := range v6 {
		condition := wtFwpmFilterCondition0{
			fieldKey:  cFWPM_CONDITION_IP_REMOTE_ADDRESS,
			matchType: cFWP_MATCH_EQUAL,
			conditionValue: wtFwpConditionValue0{
				_type: cFWP_V6_ADDR_MASK,
				value: uintptr(unsafe.Pointer(&v6[i])),
			},
		}
		if err := addPermitBothWays(session, baseObjects, weight, &condition, "Permit LAN (IPv6)", cFWPM_LAYER_ALE_AUTH_CONNECT_V6, cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V6); err != nil {
			return err
		}
	}
	return nil
}

// addPermitBothWays 同一个条件在出站与入站两层各加一条放行。
func addPermitBothWays(session uintptr, baseObjects *baseObjects, weight uint8, condition *wtFwpmFilterCondition0, name string, outLayer, inLayer windows_GUID) error {
	filter := wtFwpmFilter0{
		providerKey:         &baseObjects.provider,
		subLayerKey:         baseObjects.filters,
		weight:              filterWeight(weight),
		flags:               curFlags,
		numFilterConditions: 1,
		filterCondition:     condition,
		action:              wtFwpmAction0{_type: cFWP_ACTION_PERMIT},
	}
	filterID := uint64(0)
	for _, l := range []struct {
		layer windows_GUID
		desc  string
	}{{outLayer, name + " outbound"}, {inLayer, name + " inbound"}} {
		displayData, err := createWtFwpmDisplayData0(l.desc, "")
		if err != nil {
			return wrapErr(err)
		}
		filter.displayData = *displayData
		filter.layerKey = l.layer
		if err := fwpmFilterAdd0(session, &filter, 0, &filterID); err != nil {
			return wrapErr(err)
		}
	}
	return nil
}
