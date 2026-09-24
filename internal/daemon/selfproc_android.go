//go:build android

package daemon

// selfProcess Android 上内核跑在本应用里,按包名认(见 builder.Input.SelfProcess)。内核自己拨出的套接字经 protect
// (AutoDetectInterfaceControl)本来就不进 VPN,这几条规则在这里只是兜底;限定成本应用,别的应用去往节点同地址的
// 连接照常走隧道。(本应用别的连接——界面、订阅刷新——是进 VPN 的,见 docs/wiki/安装 Android.md。)
func selfProcess() string { return "com.maoyangui.godusevpn" }
