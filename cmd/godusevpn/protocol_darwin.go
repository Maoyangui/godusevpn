package main

// macOS 的 godusevpn:// 协议是在 .app 的 Info.plist 里用 CFBundleURLTypes 声明的(见 build/darwin/Info.plist),
// 由系统在安装时登记,不像 Windows 那样要程序自己往注册表写,所以这里什么都不用做。
func ensureURLProtocol() {}
