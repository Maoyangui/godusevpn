package paths

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// 数据目录的权限。
//
// 为什么非收不可:目录里有 config.json(完整内核配置,每个节点的地址、端口、UUID / 密码都在里面)、
// profiles\<id>.json(订阅地址,拿到就等于拿到全部节点)、settings.json 里的面板密码哈希。
// %ProgramData% 默认会把 BUILTIN\Users:(OI)(CI)(RX) 继承下来,也就是**同一台机器上任何标准用户都能直接读走**。
// 这是一条静默的凭据泄露:用户看不到任何异常。
//
// 还有一条更难察觉的:继承下来的权限里目录上带 WD(创建文件),而规则集查找的第一层就是
// <数据目录>\rulesets\<tag>.srs 且我们自己从不写那一层(见 internal/ruleset)。于是非管理员
// **不需要覆盖任何已有文件**,只要新建一个 geosite-cn.srs,就能改写以 SYSTEM 身份运行的内核认定的
// "哪些域名算国内",静默改变分流。收紧这个目录把这条路一并堵上。
//
// 但不能一收了之:界面是以**登录用户**的身份跑的(非提权),它要把服务生成的诊断包复制到桌面、
// 还要用资源管理器打开日志目录。全收紧的话这两个功能在升级之后就坏了 —— 而诊断包恰恰是出问题时
// 唯一的求助通道。所以 logs\ 与 diag\ 单独放开只读:
// 诊断包本来就是脱敏的(config.redacted.json、订阅地址打码),日志里也没有凭据,
// 这两样本来就是给人看、给人发的。
const (
	// 只有 SYSTEM 与 Administrators。真正断掉继承的是下面 SetNamedSecurityInfo 那个
	// PROTECTED_DACL_SECURITY_INFORMATION 标志 —— **少了它,继承来的 Users 读权限原样留着,
	// 加再多 ACE 也盖不掉,看着像改了其实没改**。SDDL 里的 P 是把同一个意图再写明一次。
	sddlPrivate = "D:P(A;OICI;GA;;;SY)(A;OICI;GA;;;BA)"
	// 同上,再加 BUILTIN\Users 的只读(GR 读 + GX 进目录),给界面复制诊断包和打开日志用。
	sddlReadable = "D:P(A;OICI;GA;;;SY)(A;OICI;GA;;;BA)(A;OICI;GRGX;;;BU)"
)

func secureDataDir(dir string) error {
	if err := applyDACL(dir, sddlPrivate); err != nil {
		return err
	}
	// 这两个子目录要单独设:父目录已经断了继承,它们若跟着继承就一样进不去。
	// 目录不存在就先建出来 —— diag 是用到才建的,等它自己建的时候会继承到"只有管理员"上,又坏了。
	for _, sub := range []string{Logs(), Diag()} {
		if err := os.MkdirAll(sub, 0o700); err != nil {
			return err
		}
		if err := applyDACL(sub, sddlReadable); err != nil {
			return err
		}
	}
	return nil
}

func applyDACL(dir, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("拼权限描述符: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("取权限表: %w", err)
	}
	err = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
	if err != nil {
		return fmt.Errorf("设置目录权限(%s): %w", dir, err)
	}
	return nil
}
