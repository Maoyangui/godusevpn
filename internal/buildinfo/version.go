// Package buildinfo 版本号,构建时用 -ldflags "-X .../buildinfo.Version=x.y.z" 注入。
package buildinfo

var Version = "0.1.0-dev"

// DisplayName 产品名;系统层面的标识符(服务名、管道名、目录)一律用 godusevpn。
const DisplayName = "佛跳墙"

// UserAgent 拉订阅时用:面板按 UA 判断"是客户端还是浏览器",带 sing-box 字样一定拿到订阅本体而不是落地页。
func UserAgent() string { return "godusevpn/" + Version + " (sing-box)" }
