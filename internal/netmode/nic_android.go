package netmode

// Android 上做不了这一步:应用没有权限去改物理网卡的 IPv6 配置(那是系统设置里都不给用户改的东西)。
// 隧道这一侧照旧是"把 v6 接进 VPN 再拒绝",数据包漏不出去;但网卡上运营商给的公网 v6 地址,
// 别的应用只要枚举一遍网卡仍然读得到 —— 这是 Android 的平台限制,不是配置问题。
func DisableNICIPv6(string) error { return nil }

func RestoreNICIPv6() {}

// NICIPv6Off 安卓上动不了物理网卡,永远是"没关"。
func NICIPv6Off() bool { return false }

// NICIPv6Manageable 安卓上动不了物理网卡:整套"停用网卡 IPv6"在这边是空操作,守护进程据此完全跳过,
// 免得打出做过了的假日志、又对着关不掉的移动网络反复重试。
func NICIPv6Manageable() bool { return false }
