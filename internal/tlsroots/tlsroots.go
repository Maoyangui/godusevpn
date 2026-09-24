// Package tlsroots 让本进程的 TLS 证书校验不走 Windows 的平台校验(CryptoAPI),改用一份明确的根证书池、
// 由 Go 自己校验。
//
// 为什么:Go 在 Windows 上默认把证书链交给 CertGetCertificateChain 去建。网络状态变化(TUN 起停、默认路由
// 切换、网口断开重连)和建链撞在一起时,它偶尔返回"成功"却不给链;Go 不检查,顺着空指针读,panic 展开时
// 再把空指针交给 CertFreeCertificateChain —— 系统在 crypt32 里写空指针 +0x48,进程当场退出,recover 接不住。
// 服务刚连上就要和节点握手校验证书,而 TUN 也刚起来:v0.7.5 真机验收里几次"刚连上就崩"都是这个
// (golang/go#79247,至今未修)。
//
// 做法:启动时把"内置的 Mozilla 根证书 + 本机受信任根证书存储里的证书"设成后备根证书,并由主程序的
// //go:debug x509usefallbackroots=1 强制使用。之后进程里所有"用系统根证书"的校验(net/http、sing-box 的
// 系统证书库、uTLS)拿到的都是这份普通证书池,Go 不再调用平台校验。本机根证书存储只在启动时枚举一次
// (CertEnumCertificatesInStore,不是出问题的建链),用户自己装的私有 CA 照样受信。
//
// 主程序要同时做两件事才生效:import 本包;加 //go:debug x509usefallbackroots=1。缺一样都等于没做
// (Status 会说出来,服务启动时写进日志)。
package tlsroots
