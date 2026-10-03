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

// hotspotPeers 热点 / 网络共享里连上来的设备的地址(ICS 默认 192.168.137.0/24,改过网段的也在私网里)与 DHCP 广播。
// 包级变量,理由同 lanV4。
var hotspotPeers = [...]wtFwpV4AddrAndMask{
	{addr: 0x0A000000, mask: 0xFF000000}, // 10.0.0.0/8
	{addr: 0xAC100000, mask: 0xFFF00000}, // 172.16.0.0/12
	{addr: 0xC0A80000, mask: 0xFFFF0000}, // 192.168.0.0/16
	{addr: 0xFFFFFFFF, mask: 0xFFFFFFFF}, // 255.255.255.255(DHCP 广播)
}

// 热点那几条放行的名字(测试、验收按名字认)。
const (
	hotspotDHCPInName  = "Permit hotspot DHCP request (IPv4)"
	hotspotDHCPOutName = "Permit hotspot DHCP reply (IPv4)"
	hotspotDNSName     = "Permit hotspot DNS query (IPv4)"
)

// permitHotspot 「局域网直通」关着也让本机的热点 / 网络共享能用:本机要给连上来的设备当 DHCP 服务端和 DNS 代理。
// 以前只放行本机当 DHCP 客户端,热点开着时手机连得上 Wi-Fi 却拿不到地址。这几种包都不出局域网:
//   - DHCP 服务端:收设备发来的 68→67;回 67→68 只许发往私网 / 广播 —— 不限目的的话,任何程序绑个 67 端口
//     就能往公网发 UDP;
//   - DNS 代理:私网里的设备来问本机 53,只放进来的(代理自己往外问仍受拦 DNS 管)。
//
// 设备经本机上网的流量照旧只许走隧道(转发层)。
func permitHotspot(session uintptr, baseObjects *baseObjects, weight uint8) error {
	udp := wtFwpmFilterCondition0{fieldKey: cFWPM_CONDITION_IP_PROTOCOL, matchType: cFWP_MATCH_EQUAL}
	udp.conditionValue._type, udp.conditionValue.value = cFWP_UINT8, uintptr(cIPPROTO_UDP)
	tcp := udp
	tcp.conditionValue.value = uintptr(cIPPROTO_TCP)
	port := func(field windows_GUID, p uint16) wtFwpmFilterCondition0 {
		c := wtFwpmFilterCondition0{fieldKey: field, matchType: cFWP_MATCH_EQUAL}
		c.conditionValue._type, c.conditionValue.value = cFWP_UINT16, uintptr(p)
		return c
	}
	peers := func(n int) []wtFwpmFilterCondition0 { // 同一字段的几个条件之间是"或"
		out := make([]wtFwpmFilterCondition0, n)
		for i := range out {
			out[i] = wtFwpmFilterCondition0{fieldKey: cFWPM_CONDITION_IP_REMOTE_ADDRESS, matchType: cFWP_MATCH_EQUAL}
			out[i].conditionValue._type = cFWP_V4_ADDR_MASK
			out[i].conditionValue.value = uintptr(unsafe.Pointer(&hotspotPeers[i]))
		}
		return out
	}
	for _, f := range []struct {
		layer windows_GUID
		name  string
		conds []wtFwpmFilterCondition0
	}{
		{cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4, hotspotDHCPInName,
			[]wtFwpmFilterCondition0{udp, port(cFWPM_CONDITION_IP_LOCAL_PORT, 67), port(cFWPM_CONDITION_IP_REMOTE_PORT, 68)}},
		{cFWPM_LAYER_ALE_AUTH_CONNECT_V4, hotspotDHCPOutName,
			append([]wtFwpmFilterCondition0{udp, port(cFWPM_CONDITION_IP_LOCAL_PORT, 67), port(cFWPM_CONDITION_IP_REMOTE_PORT, 68)}, peers(len(hotspotPeers))...)},
		{cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4, hotspotDNSName,
			append([]wtFwpmFilterCondition0{udp, tcp, port(cFWPM_CONDITION_IP_LOCAL_PORT, 53)}, peers(len(hotspotPeers)-1)...)},
	} {
		displayData, err := createWtFwpmDisplayData0(f.name, "")
		if err != nil {
			return wrapErr(err)
		}
		filter := wtFwpmFilter0{
			displayData:         *displayData,
			providerKey:         &baseObjects.provider,
			layerKey:            f.layer,
			subLayerKey:         baseObjects.filters,
			weight:              filterWeight(weight),
			flags:               curFlags,
			numFilterConditions: uint32(len(f.conds)),
			filterCondition:     &f.conds[0],
			action:              wtFwpmAction0{_type: cFWP_ACTION_PERMIT},
		}
		filterID := uint64(0)
		if err := fwpmFilterAdd0(session, &filter, 0, &filterID); err != nil {
			return wrapErr(err)
		}
	}
	return nil
}
