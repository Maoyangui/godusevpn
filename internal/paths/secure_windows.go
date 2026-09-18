package paths

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// secureDataDir 把 %ProgramData%\godusevpn 的权限收成"只有 SYSTEM 与 Administrators 能进"。
//
// 为什么非做不可:这个目录里有 config.json(完整内核配置,每个节点的地址、端口、UUID / 密码都在里面)、
// profiles\<id>.json(订阅地址,拿到就等于拿到全部节点)、settings.json 里的面板密码哈希,还有日志与诊断包。
// %ProgramData% 默认会把 BUILTIN\Users:(OI)(CI)(RX) 继承下来,也就是**同一台机器上任何标准用户都能直接读走**。
// 这是一条静默的凭据泄露:用户看不到任何异常。
//
// 还有一条更难察觉的:继承下来的权限里目录上带 WD(创建文件),而规则集查找的第一层就是
// <数据目录>\rulesets\<tag>.srs 且我们自己从不写那一层(见 internal/ruleset)。于是非管理员
// **不需要覆盖任何已有文件**,只要新建一个 geosite-cn.srs,就能改写以 SYSTEM 身份运行的内核认定的
// "哪些域名算国内",静默改变分流。收紧这个目录把这条路一并堵上。
//
// D:P 里的 P(protected)是关键:不带它只是往现有 DACL 里追加几条,继承来的 Users 读权限原样留着,
// 看起来改了、其实一点用没有。带上 P 会断掉继承,只保留下面列出的两条。
//   - (A;OICI;GA;;;SY) SYSTEM 完全控制,对象与容器都继承
//   - (A;OICI;GA;;;BA) Administrators 完全控制,同上
func secureDataDir(dir string) error {
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;GA;;;SY)(A;OICI;GA;;;BA)")
	if err != nil {
		return fmt.Errorf("拼数据目录的权限: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("取数据目录的权限表: %w", err)
	}
	err = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
	if err != nil {
		return fmt.Errorf("收紧数据目录权限(%s): %w", dir, err)
	}
	return nil
}
