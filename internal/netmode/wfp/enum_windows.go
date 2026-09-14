package wfp

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 生成的 zsyscall_windows.go 里没有枚举与删除,这里手写补上(签名与 fwpmu.h 一致)。
var (
	procFwpmFilterCreateEnumHandle0  = modfwpuclnt.NewProc("FwpmFilterCreateEnumHandle0")
	procFwpmFilterEnum0              = modfwpuclnt.NewProc("FwpmFilterEnum0")
	procFwpmFilterDestroyEnumHandle0 = modfwpuclnt.NewProc("FwpmFilterDestroyEnumHandle0")
	procFwpmFilterDeleteByKey0       = modfwpuclnt.NewProc("FwpmFilterDeleteByKey0")
	procFwpmSubLayerDeleteByKey0     = modfwpuclnt.NewProc("FwpmSubLayerDeleteByKey0")
	procFwpmProviderDeleteByKey0     = modfwpuclnt.NewProc("FwpmProviderDeleteByKey0")
)

// windows_GUID 给本包自己写的文件用的别名,免得每处都写全名。
type windows_GUID = windows.GUID

// wtFwpmFilterEnumTemplate0 FWPM_FILTER_ENUM_TEMPLATE0(fwpmtypes.h),字段顺序与对齐照 C 的来。
type wtFwpmFilterEnumTemplate0 struct {
	providerKey             *windows.GUID
	layerKey                windows.GUID
	enumType                uint32
	flags                   uint32
	providerContextTemplate uintptr
	numFilterConditions     uint32
	filterCondition         uintptr
	actionMask              uint32
	calloutKey              *windows.GUID
}

// 枚举参数,值照 fwpmtypes.h:
//
//	FWP_FILTER_ENUM_FLAG_BEST_TERMINATING_MATCH 0x01
//	FWP_FILTER_ENUM_FLAG_SORTED                 0x02
//	FWP_FILTER_ENUM_FLAG_BOOTTIME_ONLY          0x04
//	FWP_FILTER_ENUM_FLAG_INCLUDE_BOOTTIME       0x08
//	FWP_FILTER_ENUM_FLAG_INCLUDE_DISABLED       0x10
//
// (之前把 INCLUDE_BOOTTIME 写成了 0x02、INCLUDE_DISABLED 写成了 0x04,等于 SORTED|BOOTTIME_ONLY,
// 只枚举得到开机那组,持久那组一条都看不见,撤闸时子层就"被引用、删不掉"。)
const (
	fwpFilterEnumOverlapping         = 1
	fwpFilterEnumFlagIncludeBoottime = 0x00000008
	fwpFilterEnumFlagIncludeDisabled = 0x00000010
	enumBatch                        = 128
)

func callProc(p *windows.LazyProc, a ...uintptr) error {
	r, _, _ := p.Call(a...)
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

func fwpmFilterDeleteByKey0(engine uintptr, key *windows.GUID) error {
	return callProc(procFwpmFilterDeleteByKey0, engine, uintptr(unsafe.Pointer(key)))
}

func fwpmSubLayerDeleteByKey0(engine uintptr, key *windows.GUID) error {
	return callProc(procFwpmSubLayerDeleteByKey0, engine, uintptr(unsafe.Pointer(key)))
}

func fwpmProviderDeleteByKey0(engine uintptr, key *windows.GUID) error {
	return callProc(procFwpmProviderDeleteByKey0, engine, uintptr(unsafe.Pointer(key)))
}

// ourLayers 我们放过滤器的四个层:出站 / 入站 × IPv4 / IPv6。
func ourLayers() []windows.GUID {
	return []windows.GUID{cFWPM_LAYER_ALE_AUTH_CONNECT_V4, cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V4, cFWPM_LAYER_ALE_AUTH_CONNECT_V6, cFWPM_LAYER_ALE_AUTH_RECV_ACCEPT_V6}
}

// filterInfo 枚举到的一条我们的过滤器。
type filterInfo struct {
	key   windows.GUID
	name  string
	flags wtFwpmFilterFlags
	layer windows.GUID
}

// ourFilters 枚举我们的全部过滤器(持久的、开机的、禁用的都算)。不按提供者做模板,而是把四个层里
// 的过滤器全拉出来,凡提供者是我们的、或挂在我们子层下的都算 —— 这样哪怕某条没带提供者,撤闸时也
// 不会漏掉它,子层就一定删得掉。
func ourFilters(session uintptr) ([]filterInfo, error) {
	var out []filterInfo
	for _, layer := range ourLayers() {
		tpl := wtFwpmFilterEnumTemplate0{
			layerKey: layer, enumType: fwpFilterEnumOverlapping,
			flags: fwpFilterEnumFlagIncludeBoottime | fwpFilterEnumFlagIncludeDisabled, actionMask: 0xFFFFFFFF,
		}
		var h uintptr
		if err := callProc(procFwpmFilterCreateEnumHandle0, session, uintptr(unsafe.Pointer(&tpl)), uintptr(unsafe.Pointer(&h))); err != nil {
			return nil, wrapErr(err)
		}
		for {
			var entries **wtFwpmFilter0
			var n uint32
			if err := callProc(procFwpmFilterEnum0, session, h, enumBatch, uintptr(unsafe.Pointer(&entries)), uintptr(unsafe.Pointer(&n))); err != nil {
				_ = callProc(procFwpmFilterDestroyEnumHandle0, session, h)
				return nil, wrapErr(err)
			}
			if n == 0 {
				break
			}
			for _, f := range unsafe.Slice(entries, n) {
				ours := f.subLayerKey == sublayerKey || (f.providerKey != nil && *f.providerKey == providerKey)
				if !ours {
					continue
				}
				out = append(out, filterInfo{key: f.filterKey, name: windows.UTF16PtrToString(f.displayData.name), flags: f.flags, layer: f.layerKey})
			}
			fwpmFreeMemory0(unsafe.Pointer(&entries))
			if n < enumBatch {
				break
			}
		}
		_ = callProc(procFwpmFilterDestroyEnumHandle0, session, h)
	}
	return out, nil
}

// ourFilterKeys 我们全部过滤器的 key。
func ourFilterKeys(session uintptr) ([]windows.GUID, error) {
	fs, err := ourFilters(session)
	if err != nil {
		return nil, err
	}
	keys := make([]windows.GUID, len(fs))
	for i := range fs {
		keys[i] = fs[i].key
	}
	return keys, nil
}
