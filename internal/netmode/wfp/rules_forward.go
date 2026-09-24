//go:build windows

package wfp

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// IP 转发层的闸:管的是**经本机转发**的流量 —— 热点共享(ICS)、网络共享把手机的包送进本机再转出去。
//
// 本机 socket 那四个 ALE 层看不见转发的包:它们只在本机程序发起 / 接受连接时判一次。热点设备的流量
// 能进隧道,靠的是路由表把它送到隧道网卡;隧道网卡一旦不在(内核崩了、在重启),路由表退回默认路由,
// 这些包就顺着物理网卡明文出去,ALE 层的闸对此无能为力。所以转发层单独放一组:
//   - 默认全拦(权重 0,持久 + 开机两组都装);
//   - 隧道网卡在的时候按接口号放行两个方向(去往隧道、从隧道回来),见 TunUp —— 网卡不在,接口号对不上,自然全拦;
//   - 「局域网直通」开着时放行去往私网的转发(热点设备访问局域网里的打印机、NAS)。
// 这样隧道挂了热点设备也一起断网,和本机一个待遇。

// 转发层的接口条件用 UINT32 接口号(FWPM_CONDITION_SOURCE_INTERFACE_INDEX / DESTINATION_INTERFACE_INDEX);
// 常量与数据类型已在本机 WFP 引擎上按 layerId 8 / 10 逐字段核对过(见 types_windows.go 的注释)。

const forwardTunPermitName = "Permit forward via tunnel"

// blockForward 转发层默认全拦。
func blockForward(session uintptr, baseObjects *baseObjects, weight uint8) error {
	for _, l := range []struct {
		layer windows.GUID
		name  string
	}{{cFWPM_LAYER_IPFORWARD_V4, "Block all forwarding (IPv4)"}, {cFWPM_LAYER_IPFORWARD_V6, "Block all forwarding (IPv6)"}} {
		displayData, err := createWtFwpmDisplayData0(l.name, "")
		if err != nil {
			return wrapErr(err)
		}
		filter := wtFwpmFilter0{
			displayData: *displayData,
			providerKey: &baseObjects.provider,
			layerKey:    l.layer,
			subLayerKey: baseObjects.filters,
			weight:      filterWeight(weight),
			flags:       curFlags,
			action:      wtFwpmAction0{_type: cFWP_ACTION_BLOCK},
		}
		filterID := uint64(0)
		if err := fwpmFilterAdd0(session, &filter, 0, &filterID); err != nil {
			return wrapErr(err)
		}
	}
	return nil
}

// permitForwardLAN 放行去往私网 / 本地段的转发(热点设备访问局域网),两个地址族各一组。
func permitForwardLAN(session uintptr, baseObjects *baseObjects, weight uint8) error {
	v4, v6 := &lanV4, &lanV6 // 包级变量,见 rules_lan.go:条件值以 uintptr 交给 DLL,不能留在协程栈上
	for i := range v4 {
		c := wtFwpmFilterCondition0{
			fieldKey:  cFWPM_CONDITION_IP_DESTINATION_ADDRESS,
			matchType: cFWP_MATCH_EQUAL,
			conditionValue: wtFwpConditionValue0{
				_type: cFWP_V4_ADDR_MASK,
				value: uintptr(unsafe.Pointer(&v4[i])),
			},
		}
		if err := addPermitForward(session, baseObjects, weight, &c, "Permit forward to LAN (IPv4)", cFWPM_LAYER_IPFORWARD_V4); err != nil {
			return err
		}
	}
	for i := range v6 {
		c := wtFwpmFilterCondition0{
			fieldKey:  cFWPM_CONDITION_IP_DESTINATION_ADDRESS,
			matchType: cFWP_MATCH_EQUAL,
			conditionValue: wtFwpConditionValue0{
				_type: cFWP_V6_ADDR_MASK,
				value: uintptr(unsafe.Pointer(&v6[i])),
			},
		}
		if err := addPermitForward(session, baseObjects, weight, &c, "Permit forward to LAN (IPv6)", cFWPM_LAYER_IPFORWARD_V6); err != nil {
			return err
		}
	}
	return nil
}

// permitForwardTun 放行进出隧道网卡的转发:目的接口是它(去往隧道)、或源接口是它(从隧道回来)。
// 这组不带开机标志(开机时没有隧道网卡),而且每次隧道网卡重建都要按新接口号重装,所以名字固定,便于先删后加。
func permitForwardTun(session uintptr, baseObjects *baseObjects, weight uint8, ifIndex uint32) error {
	for _, layer := range []windows.GUID{cFWPM_LAYER_IPFORWARD_V4, cFWPM_LAYER_IPFORWARD_V6} {
		for _, field := range []windows.GUID{cFWPM_CONDITION_DESTINATION_INTERFACE_INDEX, cFWPM_CONDITION_SOURCE_INTERFACE_INDEX} {
			c := wtFwpmFilterCondition0{
				fieldKey:  field,
				matchType: cFWP_MATCH_EQUAL,
				conditionValue: wtFwpConditionValue0{
					_type: cFWP_UINT32,
					value: uintptr(ifIndex),
				},
			}
			if err := addPermitForward(session, baseObjects, weight, &c, forwardTunPermitName, layer); err != nil {
				return err
			}
		}
	}
	return nil
}

func addPermitForward(session uintptr, baseObjects *baseObjects, weight uint8, condition *wtFwpmFilterCondition0, name string, layer windows.GUID) error {
	displayData, err := createWtFwpmDisplayData0(name, "")
	if err != nil {
		return wrapErr(err)
	}
	filter := wtFwpmFilter0{
		displayData:         *displayData,
		providerKey:         &baseObjects.provider,
		layerKey:            layer,
		subLayerKey:         baseObjects.filters,
		weight:              filterWeight(weight),
		flags:               curFlags,
		numFilterConditions: 1,
		filterCondition:     condition,
		action:              wtFwpmAction0{_type: cFWP_ACTION_PERMIT},
	}
	filterID := uint64(0)
	if err := fwpmFilterAdd0(session, &filter, 0, &filterID); err != nil {
		return wrapErr(err)
	}
	return nil
}

// TunUp 隧道网卡起来了:把转发层"经隧道放行"那组按新接口号重装(先删旧的)。闸没开时什么都不做。
// 装成持久的,和闸的其它过滤器一样跨进程、跨重启;网卡没了接口号就对不上,等于自动收回。
func TunUp(ifIndex uint32) error {
	mu.Lock()
	defer mu.Unlock()
	s, err := openSession()
	if err != nil {
		return err
	}
	defer fwpmEngineClose0(s)
	all, err := ourFilters(s)
	if err != nil {
		return err
	}
	if len(all) == 0 {
		return nil // 闸没开
	}
	return runTransaction(s, func(s uintptr) error {
		for i := range all {
			if all[i].name == forwardTunPermitName {
				if err := fwpmFilterDeleteByKey0(s, &all[i].key); err != nil && !notFound(err) {
					return wrapErr(err)
				}
			}
		}
		curFlags = cFWPM_FILTER_FLAG_PERSISTENT
		return permitForwardTun(s, base, 12, ifIndex)
	})
}
