//go:build windows

package wfp

import "unsafe"

// permitLAN 放行去往局域网 / 私网 / 本地多播的流量:这些不出网,不算漏。
// 地址掩码按 WFP 的 FWP_V4_ADDR_AND_MASK / FWP_V6_ADDR_AND_MASK 给,v4 是主机字节序。
func permitLAN(session uintptr, baseObjects *baseObjects, weight uint8) error {
	v4 := []wtFwpV4AddrAndMask{
		{addr: 0x0A000000, mask: 0xFF000000}, // 10.0.0.0/8
		{addr: 0xAC100000, mask: 0xFFF00000}, // 172.16.0.0/12
		{addr: 0xC0A80000, mask: 0xFFFF0000}, // 192.168.0.0/16
		{addr: 0xA9FE0000, mask: 0xFFFF0000}, // 169.254.0.0/16
		{addr: 0xE0000000, mask: 0xF0000000}, // 224.0.0.0/4
		{addr: 0xFFFFFFFF, mask: 0xFFFFFFFF}, // 255.255.255.255
	}
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
	v6 := []wtFwpV6AddrAndMask{
		{addr: [16]uint8{0xfe, 0x80}, prefixLength: 10}, // fe80::/10
		{addr: [16]uint8{0xff}, prefixLength: 8},        // ff00::/8
		{addr: [16]uint8{0xfc}, prefixLength: 7},        // fc00::/7
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
