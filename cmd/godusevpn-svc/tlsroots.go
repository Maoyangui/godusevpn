//go:debug x509usefallbackroots=1

package main

// Windows 上证书校验不走平台校验(CryptoAPI),改用内置 + 本机根证书、由 Go 自己校验 —— 见 internal/tlsroots:
// 网络变化时平台校验偶尔"成功但不给链",Go 顺着空指针崩在 crypt32 里。两样缺一不可:import 那个包,
// 加上面这行 //go:debug。其它平台上两样都不起作用。
import _ "github.com/Maoyangui/godusevpn/internal/tlsroots"
