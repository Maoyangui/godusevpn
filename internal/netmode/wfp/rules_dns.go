//go:build windows

package wfp

import "unsafe"

// 拦 DNS 那两条过滤器的名字。guardCovers 按名字认它们(我们自己装的,名字固定)。
const (
	dnsBlockName4 = "Block DNS (IPv4)"
	dnsBlockName6 = "Block DNS (IPv6)"
)

// dnsBlockPorts DNS(53)与 DNS over TLS / QUIC(853)。
var dnsBlockPorts = []uint16{53, 853}

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
	// 同一字段的几个条件之间是"或",不同字段之间是"且":(UDP 或 TCP)且(端口 53 或 853)。
	conditions := make([]wtFwpmFilterCondition0, 0, 2+len(dnsBlockPorts))
	for _, proto := range []wtIPProto{cIPPROTO_UDP, cIPPROTO_TCP} {
		c := wtFwpmFilterCondition0{fieldKey: cFWPM_CONDITION_IP_PROTOCOL, matchType: cFWP_MATCH_EQUAL}
		c.conditionValue._type = cFWP_UINT8
		c.conditionValue.value = uintptr(proto)
		conditions = append(conditions, c)
	}
	for _, port := range dnsBlockPorts {
		c := wtFwpmFilterCondition0{fieldKey: cFWPM_CONDITION_IP_REMOTE_PORT, matchType: cFWP_MATCH_EQUAL}
		c.conditionValue._type = cFWP_UINT16
		c.conditionValue.value = uintptr(port)
		conditions = append(conditions, c)
	}
	for _, l := range []struct {
		layer windows_GUID
		name  string
	}{
		{cFWPM_LAYER_ALE_AUTH_CONNECT_V4, dnsBlockName4},
		{cFWPM_LAYER_ALE_AUTH_CONNECT_V6, dnsBlockName6},
	} {
		displayData, err := createWtFwpmDisplayData0(l.name, "")
		if err != nil {
			return wrapErr(err)
		}
		filter := wtFwpmFilter0{
			displayData:         *displayData,
			providerKey:         &baseObjects.provider,
			layerKey:            l.layer,
			subLayerKey:         baseObjects.filters,
			weight:              filterWeight(weight),
			flags:               curFlags,
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
