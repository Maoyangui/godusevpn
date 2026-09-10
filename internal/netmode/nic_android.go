package netmode

// Android 上做不了这一步:应用没有权限去改物理网卡的 IPv6 配置(那是系统设置里都不给用户改的东西)。
// 隧道这一侧照旧是"把 v6 接进 VPN 再拒绝",数据包漏不出去;但网卡上运营商给的公网 v6 地址,
// 别的应用只要枚举一遍网卡仍然读得到 —— 这是 Android 的平台限制,不是配置问题。
func DisableNICIPv6(string) error { return nil }

func RestoreNICIPv6() {}
