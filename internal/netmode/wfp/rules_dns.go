//go:build windows

package wfp

import "unsafe"

// 按端口拦的那几条过滤器的名字。guardCovers 按名字认它们(我们自己装的,名字固定)。
const (
	dnsBlockName4     = "Block DNS (IPv4)"
	dnsBlockName6     = "Block DNS (IPv6)"
	upnpBlockOutName4 = "Block UPnP / NAT-PMP outbound (IPv4)"
	upnpBlockOutName6 = "Block UPnP / NAT-PMP outbound (IPv6)"
	upnpBlockInName4  = "Block UPnP / NAT-PMP inbound (IPv4)"
	upnpBlockInName6  = "Block UPnP / NAT-PMP inbound (IPv6)"
	fakeIP6BlockName  = "Block fake-ip range (IPv6)"
)

// portBlock 一条按协议 + 端口拦的过滤器:装在哪一层、叫什么、比远端还是本地端口。
type portBlock struct {
	layer windows_GUID
	name  string
	field windows_GUID // cFWPM_CONDITION_IP_REMOTE_PORT / cFWPM_CONDITION_IP_LOCAL_PORT
}

var (
	// dnsBlockPorts DNS(53)与 DNS over TLS / QUIC(853)。
	dnsBlockPorts = []uint16{53, 853}
	dnsBlocks     = []portBlock{
		{cFWPM_LAYER_ALE_AUTH_CONNECT_V4, dnsBlockName4, cFWPM_CONDITION_IP_REMOTE_PORT},
		{cFWPM_LAYER_ALE_AUTH_CONNECT_V6, dnsBlockName6, cFWPM_CONDITION_IP_REMOTE_PORT},
	}

	// 发出去:SSDP 发现(1900,含组播 239.255.255.250 / ff02::c)与 NAT-PMP / PCP 请求(发往网关 5351)。
	upnpOutPorts  = []uint16{1900, 5351}
	upnpOutBlocks = []portBlock{
		{cFWPM_LAYER_ALE_AUTH_CONNECT_V4, upnpBlockOutName4, cFWPM_CONDITION_IP_REMOTE_PORT},
		{cFWPM_LAYER_ALE_AUTH_CONNECT_V6, upnpBlockOutName6, cFWPM_CONDITION_IP_REMOTE_PORT},
	}
	// 收进来:路由器主动发的 SSDP 通告(发往 1900)与 NAT-PMP / PCP 地址通告(发往 224.0.0.1 / ff02::1 的 5350,
	// 里面直接带着公网地址)—— 只拦发出去的话,程序在这两个端口上听着照样能拿到。
	upnpInPorts  = []uint16{1900, 5350}
	upnpInBlocks = []portBlock{
		{cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4, upnpBlockInName4, cFWPM_CONDITION_IP_LOCAL_PORT},
		{cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V6, upnpBlockInName6, cFWPM_CONDITION_IP_LOCAL_PORT},
	}
)

// namedBlocks guardCovers 要求齐全的那几条按端口拦的过滤器。
func namedBlocks() []portBlock {
	all := append([]portBlock{}, dnsBlocks...)
	all = append(all, upnpOutBlocks...)
	all = append(all, upnpInBlocks...)
	return append(all, portBlock{cFWPM_LAYER_ALE_AUTH_CONNECT_V6, fakeIP6BlockName, cFWPM_CONDITION_IP_REMOTE_ADDRESS})
}

// fakeIP6 客户端发给应用的 v6 假地址段(builder 的 fakeIP6,fc00::/18),落在局域网放行的 fc00::/7 里。
// 包级变量:条件值以 uintptr 交给 DLL,理由同 lanV4。
var fakeIP6 = wtFwpV6AddrAndMask{addr: [16]uint8{0xfc}, prefixLength: 18}

// blockFakeIP6 压在局域网放行上面拦掉发往 v6 假地址段的包。开着 IPv6 + fake-ip 时,隧道断开的空档里应用还会往
// 记下的假地址发包,局域网放行会把它们发给路由器 —— 假地址没有真实目的地,内容出不去,但包不该离开本机。
// 经隧道的(本机地址是隧道地址)、回环、本服务都在它上面放行,连着时不受影响。
func blockFakeIP6(session uintptr, baseObjects *baseObjects, weight uint8) error {
	conditions := []wtFwpmFilterCondition0{{fieldKey: cFWPM_CONDITION_IP_REMOTE_ADDRESS, matchType: cFWP_MATCH_EQUAL}}
	conditions[0].conditionValue._type = cFWP_V6_ADDR_MASK
	conditions[0].conditionValue.value = uintptr(unsafe.Pointer(&fakeIP6))
	displayData, err := createWtFwpmDisplayData0(fakeIP6BlockName, "")
	if err != nil {
		return wrapErr(err)
	}
	filter := wtFwpmFilter0{
		displayData:         *displayData,
		providerKey:         &baseObjects.provider,
		layerKey:            cFWPM_LAYER_ALE_AUTH_CONNECT_V6,
		subLayerKey:         baseObjects.filters,
		weight:              filterWeight(weight),
		flags:               blockFlags(),
		numFilterConditions: 1,
		filterCondition:     &conditions[0],
		action:              wtFwpmAction0{_type: cFWP_ACTION_BLOCK},
	}
	filterID := uint64(0)
	if err := fwpmFilterAdd0(session, &filter, 0, &filterID); err != nil {
		return wrapErr(err)
	}
	return nil
}

// blockDNS 拦掉发往任何地址的 DNS,TCP 和 UDP 都拦。
//
// 为什么要有:隧道断开的空档(服务重启、升级、崩溃、断线重连)里只剩这组持久闸。"局域网直通"开着时它放行
// 去往私网的一切流量,而物理网卡上配的 DNS 通常就是路由器(私网地址)—— Windows 改问它,查询经路由器转给
// 运营商,域名就出了隧道。连上之后 sing-box 的严格路由另有一组拦 53 的规则,但它随内核一起没了。
//
// 权重:高于局域网放行(12),低于放行本服务(15)、回环与隧道地址(14)。所以经隧道的 DNS(本机地址是隧道
// 地址,包括应用直接问 8.8.8.8 被劫持的那种)、回环上的 DNS、本服务自己解析节点域名,都在它上面放行。
//
// 转发层(热点共享)没有端口字段,按端口拦不了:经本机转发、直接问局域网 DNS 的查询不归这组管。
func blockDNS(session uintptr, baseObjects *baseObjects, weight uint8) error {
	return addPortBlocks(session, baseObjects, weight, []wtIPProto{cIPPROTO_UDP, cIPPROTO_TCP}, dnsBlockPorts, dnsBlocks)
}

// blockUPnP 严格全局下,"局域网直通"也不放行 UPnP 发现(SSDP)与 NAT-PMP / PCP:任何程序都能靠它们向路由器
// 问到宽带的公网 IPv4(IGD 的 GetExternalIPAddress、NAT-PMP 的外部地址),再经隧道报出去 —— 包没漏,
// 真实地址漏了。代价:连着的时候局域网投屏、设备自动发现(DLNA 等)不可用,手动填 IP 访问照常。
// 权重与 blockDNS 相同:压在局域网放行上面,经隧道的(本机地址是隧道地址)、回环、本服务都在它上面放行。
func blockUPnP(session uintptr, baseObjects *baseObjects, weight uint8) error {
	udp := []wtIPProto{cIPPROTO_UDP}
	if err := addPortBlocks(session, baseObjects, weight, udp, upnpOutPorts, upnpOutBlocks); err != nil {
		return err
	}
	return addPortBlocks(session, baseObjects, weight, udp, upnpInPorts, upnpInBlocks)
}

// addPortBlocks 同一组(协议, 端口)在每个 portBlock 那一层各装一条拦截。
// 同一字段的几个条件之间是"或",不同字段之间是"且":(协议之一)且(端口之一)。条件值都是直接放在
// uintptr 里的整数,不指向任何内存。
func addPortBlocks(session uintptr, baseObjects *baseObjects, weight uint8, protos []wtIPProto, ports []uint16, blocks []portBlock) error {
	for _, b := range blocks {
		conditions := make([]wtFwpmFilterCondition0, 0, len(protos)+len(ports))
		for _, proto := range protos {
			c := wtFwpmFilterCondition0{fieldKey: cFWPM_CONDITION_IP_PROTOCOL, matchType: cFWP_MATCH_EQUAL}
			c.conditionValue._type = cFWP_UINT8
			c.conditionValue.value = uintptr(proto)
			conditions = append(conditions, c)
		}
		for _, port := range ports {
			c := wtFwpmFilterCondition0{fieldKey: b.field, matchType: cFWP_MATCH_EQUAL}
			c.conditionValue._type = cFWP_UINT16
			c.conditionValue.value = uintptr(port)
			conditions = append(conditions, c)
		}
		displayData, err := createWtFwpmDisplayData0(b.name, "")
		if err != nil {
			return wrapErr(err)
		}
		filter := wtFwpmFilter0{
			displayData:         *displayData,
			providerKey:         &baseObjects.provider,
			layerKey:            b.layer,
			subLayerKey:         baseObjects.filters,
			weight:              filterWeight(weight),
			flags:               blockFlags(),
			numFilterConditions: uint32(len(conditions)),
			filterCondition:     (*wtFwpmFilterCondition0)(unsafe.Pointer(&conditions[0])),
			action: wtFwpmAction0{
				_type: cFWP_ACTION_BLOCK,
			},
		}
		filterID := uint64(0)
		if err := fwpmFilterAdd0(session, &filter, 0, &filterID); err != nil {
			return wrapErr(err)
		}
	}
	return nil
}
