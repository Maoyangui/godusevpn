package main

// 落地页的一键导入靠 godusevpn:// 协议。安装包会往 HKLM 写一份,但 0.6.1-a7 之前的安装包漏了这一段,
// 光靠重装才能补上;所以客户端每次启动自己再确认一遍,写在 HKCU 下(不需要管理员,且优先于机器级)。
// 指向的永远是当前这个 exe,换了安装位置也跟着更新。

import (
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const protoScheme = "godusevpn"

// ensureURLProtocol 确认 godusevpn:// 指向本程序;已经对上就什么都不做。
func ensureURLProtocol() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	ensureScheme(protoScheme, exe)
}

// ensureScheme 把 <scheme>:// 注册到当前用户名下,指向 exe。
func ensureScheme(scheme, exe string) {
	want := `"` + exe + `" "%1"`
	base := `Software\Classes\` + scheme
	if cur, err := readString(base+`\shell\open\command`, ""); err == nil && strings.EqualFold(cur, want) {
		return
	}
	set := func(path, name, value string) bool {
		k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.SET_VALUE)
		if err != nil {
			return false
		}
		defer k.Close()
		return k.SetStringValue(name, value) == nil
	}
	if !set(base, "", "URL:"+scheme+" Protocol") {
		return // 没权限就算了,一键导入退回到手动粘贴地址
	}
	set(base, "URL Protocol", "")
	set(base+`\DefaultIcon`, "", exe+",0")
	set(base+`\shell\open\command`, "", want)
}

func readString(path, name string) (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	s, _, err := k.GetStringValue(name)
	return s, err
}
