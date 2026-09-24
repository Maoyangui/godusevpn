//go:build android

package daemon

// selfProcess Android 上内核跑在本应用里,按包名认(见 builder.Input.SelfProcess)。本应用自己的连接经 protect
// 本来就不进 VPN,这几条规则在这里只是兜底;限定成本应用,别的应用去往节点同地址的连接照常走隧道。
func selfProcess() string { return "com.maoyangui.godusevpn" }
