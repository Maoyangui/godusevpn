//go:build windows

package wfp

import (
	"encoding/binary"
	"net/netip"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 规则模式那几条(只进运行期那组,开机那组没有):内核不在的时候,防火墙照着规则放行直连。
//
// 权重(接着 installSet 的表):
//
//	12 局域网、DHCP、邻居发现、热点      11 规则明确走隧道的端口      10 直连规则的地址段、入站
//	 9 DNS、UPnP / NAT-PMP(放行段以外的才轮到它们)      0 其余全拦
//
// 规则模式下 DNS 不压在放行上面:物理网卡上的 DNS 通常是路由器或运营商,拦了直连的网站就解析不了;
// 发往放行段以外的 DNS 照样被拦。局域网投屏、设备发现(UPnP)也照常,规则模式本来就有直连。

const (
	directPermitName4 = "Permit direct rules (IPv4)"
	directPermitName6 = "Permit direct rules (IPv6)"
	forwardPermitName = "Permit forward by direct rules"
	inboundPermitName = "Permit inbound (rule mode)"
	tunnelPortsName   = "Block tunnel ports"
	ruleDNSName       = "Permit DNS (rule mode)"

	// directBatch 一条过滤器里放多少个地址条件(同一字段的几个条件之间是"或")。国内地址段有几千个,一段一条的话
	// 持久存储和枚举都很重;只在建连接时判一次(转发层例外,但隧道在的时候它排不到这里)。
	directBatch = 256
)

// PortRange 一个端口或端口段。
type PortRange struct{ Lo, Hi uint16 }

// directAddrs 把地址段转成 WFP 的结构。
func directAddrs(prefixes []netip.Prefix) (v4 []wtFwpV4AddrAndMask, v6 []wtFwpV6AddrAndMask) {
	for _, p := range prefixes {
		if p.Addr().Is4() {
			a := p.Addr().As4()
			v4 = append(v4, wtFwpV4AddrAndMask{addr: binary.BigEndian.Uint32(a[:]), mask: v4Mask(p.Bits())})
		} else {
			v6 = append(v6, wtFwpV6AddrAndMask{addr: p.Addr().As16(), prefixLength: uint8(p.Bits())})
		}
	}
	return v4, v6
}

func v4Mask(bits int) uint32 {
	if bits <= 0 {
		return 0
	}
	return ^uint32(0) << (32 - bits)
}

// permitDirect 放行直连规则的地址段:field 是比对的地址字段,按批分到几条过滤器里。
func permitDirect(session uintptr, baseObjects *baseObjects, weight uint8, prefixes []netip.Prefix, field, layer4, layer6 windows.GUID, name4, name6 string) error {
	v4, v6 := directAddrs(prefixes)
	// 条件值以 uintptr 交给 DLL,编译器和垃圾回收都看不见这个引用:钉住(理由同 lanV4)。
	var pin runtime.Pinner
	defer pin.Unpin()
	if len(v4) > 0 {
		pin.Pin(&v4[0])
	}
	if len(v6) > 0 {
		pin.Pin(&v6[0])
	}
	for i := 0; i < len(v4); i += directBatch {
		batch := v4[i:min(i+directBatch, len(v4))]
		conds := make([]wtFwpmFilterCondition0, len(batch))
		for j := range batch {
			conds[j] = wtFwpmFilterCondition0{fieldKey: field, matchType: cFWP_MATCH_EQUAL}
			conds[j].conditionValue._type = cFWP_V4_ADDR_MASK
			conds[j].conditionValue.value = uintptr(unsafe.Pointer(&batch[j]))
		}
		if err := addConditionFilter(session, baseObjects, weight, layer4, name4, conds, cFWP_ACTION_PERMIT, curFlags, &pin); err != nil {
			return err
		}
	}
	for i := 0; i < len(v6); i += directBatch {
		batch := v6[i:min(i+directBatch, len(v6))]
		conds := make([]wtFwpmFilterCondition0, len(batch))
		for j := range batch {
			conds[j] = wtFwpmFilterCondition0{fieldKey: field, matchType: cFWP_MATCH_EQUAL}
			conds[j].conditionValue._type = cFWP_V6_ADDR_MASK
			conds[j].conditionValue.value = uintptr(unsafe.Pointer(&batch[j]))
		}
		if err := addConditionFilter(session, baseObjects, weight, layer6, name6, conds, cFWP_ACTION_PERMIT, curFlags, &pin); err != nil {
			return err
		}
	}
	return nil
}

// addConditionFilter 装一条带一组条件的过滤器。
func addConditionFilter(session uintptr, baseObjects *baseObjects, weight uint8, layer windows.GUID, name string, conds []wtFwpmFilterCondition0, action wtFwpActionType, flags wtFwpmFilterFlags, pin *runtime.Pinner) error {
	displayData, err := createWtFwpmDisplayData0(name, "")
	if err != nil {
		return wrapErr(err)
	}
	filter := wtFwpmFilter0{
		displayData: *displayData,
		providerKey: &baseObjects.provider,
		layerKey:    layer,
		subLayerKey: baseObjects.filters,
		weight:      filterWeight(weight),
		flags:       flags,
		action:      wtFwpmAction0{_type: action},
	}
	if len(conds) > 0 {
		pin.Pin(&conds[0])
		filter.numFilterConditions = uint32(len(conds))
		filter.filterCondition = &conds[0]
	}
	filterID := uint64(0)
	if err := fwpmFilterAdd0(session, &filter, 0, &filterID); err != nil {
		return wrapErr(err)
	}
	return nil
}

// permitInbound 规则模式不拦入站:别人连本机的服务(远程桌面、游戏联机)不是"该走隧道的流量逃出去",
// 规则管的是本机去哪儿。按端口拦的 UPnP / NAT-PMP 通告也一起放行了(见文件开头)。
func permitInbound(session uintptr, baseObjects *baseObjects, weight uint8) error {
	var pin runtime.Pinner
	defer pin.Unpin()
	for _, layer := range []windows.GUID{cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4, cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V6} {
		if err := addConditionFilter(session, baseObjects, weight, layer, inboundPermitName, nil, cFWP_ACTION_PERMIT, curFlags, &pin); err != nil {
			return err
		}
	}
	return nil
}

// permitRuleDNS 规则模式放行 53 端口,不看目的地址:系统配的解析器不一定在放行段里(8.8.8.8、云厂商的解析器),
// 拦了隧道一断直连的网站就解析不了。(TCP 或 UDP)且端口 53。
func permitRuleDNS(session uintptr, baseObjects *baseObjects, weight uint8) error {
	var pin runtime.Pinner
	defer pin.Unpin()
	conds := make([]wtFwpmFilterCondition0, 0, 3)
	for _, proto := range []wtIPProto{cIPPROTO_TCP, cIPPROTO_UDP} {
		c := wtFwpmFilterCondition0{fieldKey: cFWPM_CONDITION_IP_PROTOCOL, matchType: cFWP_MATCH_EQUAL}
		c.conditionValue._type = cFWP_UINT8
		c.conditionValue.value = uintptr(proto)
		conds = append(conds, c)
	}
	port := wtFwpmFilterCondition0{fieldKey: cFWPM_CONDITION_IP_REMOTE_PORT, matchType: cFWP_MATCH_EQUAL}
	port.conditionValue._type = cFWP_UINT16
	port.conditionValue.value = uintptr(53)
	conds = append(conds, port)
	for _, layer := range []windows.GUID{cFWPM_LAYER_ALE_AUTH_CONNECT_V4, cFWPM_LAYER_ALE_AUTH_CONNECT_V6} {
		if err := addConditionFilter(session, baseObjects, weight, layer, ruleDNSName, conds, cFWP_ACTION_PERMIT, curFlags, &pin); err != nil {
			return err
		}
	}
	return nil
}

// blockTunnelPorts 规则明确走隧道的端口:压在放行段上面,这些端口去哪都不放(局域网除外,它在更上面)。
// (TCP 或 UDP)且(端口之一)。单个端口直接比值,端口段用 FWP_RANGE。
func blockTunnelPorts(session uintptr, baseObjects *baseObjects, weight uint8, ports []PortRange) error {
	if len(ports) == 0 {
		return nil
	}
	ranges := make([]wtFwpRange0, len(ports))
	var pin runtime.Pinner
	defer pin.Unpin()
	pin.Pin(&ranges[0])
	conds := make([]wtFwpmFilterCondition0, 0, len(ports)+2)
	for _, proto := range []wtIPProto{cIPPROTO_TCP, cIPPROTO_UDP} {
		c := wtFwpmFilterCondition0{fieldKey: cFWPM_CONDITION_IP_PROTOCOL, matchType: cFWP_MATCH_EQUAL}
		c.conditionValue._type = cFWP_UINT8
		c.conditionValue.value = uintptr(proto)
		conds = append(conds, c)
	}
	for i, p := range ports {
		c := wtFwpmFilterCondition0{fieldKey: cFWPM_CONDITION_IP_REMOTE_PORT, matchType: cFWP_MATCH_EQUAL}
		if p.Lo == p.Hi {
			c.conditionValue._type = cFWP_UINT16
			c.conditionValue.value = uintptr(p.Lo)
		} else {
			ranges[i] = wtFwpRange0{
				valueLow:  wtFwpValue0{_type: cFWP_UINT16, value: uintptr(p.Lo)},
				valueHigh: wtFwpValue0{_type: cFWP_UINT16, value: uintptr(p.Hi)},
			}
			c.matchType = cFWP_MATCH_RANGE
			c.conditionValue._type = cFWP_RANGE_TYPE
			c.conditionValue.value = uintptr(unsafe.Pointer(&ranges[i]))
		}
		conds = append(conds, c)
	}
	for _, layer := range []windows.GUID{cFWPM_LAYER_ALE_AUTH_CONNECT_V4, cFWPM_LAYER_ALE_AUTH_CONNECT_V6} {
		if err := addConditionFilter(session, baseObjects, weight, layer, tunnelPortsName, conds, cFWP_ACTION_BLOCK, blockFlags(), &pin); err != nil {
			return err
		}
	}
	return nil
}
