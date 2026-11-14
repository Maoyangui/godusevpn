package update

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// 安装包目录的清理。以前每次升级都往同一个目录下一个新安装包,旧的从来不删:
// 一台 Windows 电脑的 %TEMP%\godusevpn-update 里攒了 31 个(0.6.3 到 0.7.16,约 500 MB),
// 电视盒子、路由器这种本来就小的存储上更要命。现在两处清:
//   - 下新版之前(Download):目录里别的版本的安装包、macOS 解压用的 unpacked 都删掉,只留这次下的;
//   - 新版跑起来之后(CleanStale,桌面程序和守护进程启动时调):装过的(当前版本)和更旧的都删掉,
//     比当前版本新的留着 —— 可能是刚下好、还在等系统安装器装的那一个(Android 上用户还没在安装界面点确认)。
// 这两个目录都是更新专用的(%TEMP%\godusevpn-update、数据目录下的 update),Download 只删认得出的安装包
// 和 unpacked;CleanStale 在当前版本号认得出来时把目录里不是"更新的安装包"的东西都清掉。

// installerOS 发布资产名里系统那一段(见 assetName;darwin 是老名字,留着认)。
var installerOS = []string{"windows", "macos", "darwin", "linux", "android"}

// installerVersion 从发布资产名 godusevpn-<版本>-<系统>-... 里取出版本号;不是安装包返回 false。
// 版本号自己可以带横杠(0.7.17-rc.1),所以从左往右试每个横杠:左边是合法版本号、右边以系统名开头才算。
func installerVersion(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, "godusevpn-")
	if !ok {
		return "", false
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] != '-' {
			continue
		}
		ver, tail := rest[:i], rest[i+1:]
		if _, _, ok := parse(ver); !ok || strings.HasPrefix(ver, "v") {
			continue
		}
		for _, osName := range installerOS {
			if strings.HasPrefix(tail, osName+"-") {
				return ver, true
			}
		}
	}
	return "", false
}

// removeOtherInstallers 下载 keep 之前,把 dir 里别的安装包和 unpacked 删掉(删不掉的不管:比如还开着的那个)。
func removeOtherInstallers(dir, keep string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		name := e.Name()
		if name == keep {
			continue
		}
		if _, ok := installerVersion(name); ok || name == "unpacked" {
			_ = os.RemoveAll(filepath.Join(dir, name))
		}
	}
}

// CleanStale 新版跑起来之后清理安装包目录:当前版本(current)和更旧的安装包、unpacked 以及别的杂物都删掉,
// 只留比当前版本新的安装包。current 认不出来(本地构建、dev)时什么都不删,免得把等着装的新版删掉。
// 目录不存在不算错;单个文件删不掉(Windows 上安装器还开着它)跳过,下次启动再删。
func CleanStale(dir, current string) error {
	if _, _, ok := parse(current); !ok {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range ents {
		name := e.Name()
		if v, ok := installerVersion(name); ok && Newer(v, current) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, name))
	}
	return nil
}
