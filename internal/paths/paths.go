package paths

import (
	"os"
	"path/filepath"
)

// AppName 目录名与服务名,三端一致。
const AppName = "godusevpn"

func Settings() string { return filepath.Join(ConfDir(), "settings.json") }

// ProfileCache 某条订阅的节点缓存。
func ProfileCache(id string) string { return filepath.Join(DataDir(), "profiles", id+".json") }

// LegacyProfileCache schema 1 时代的单订阅缓存,迁移时读一次。
func LegacyProfileCache() string { return filepath.Join(DataDir(), "profile.json") }

func Config() string   { return filepath.Join(DataDir(), "config.json") }
func LastGood() string { return filepath.Join(DataDir(), "config.last-good.json") }
func State() string    { return filepath.Join(DataDir(), "state.json") }
func CacheDB() string  { return filepath.Join(DataDir(), "cache.db") }
func Logs() string     { return filepath.Join(DataDir(), "logs") }
func RuleSets() string { return filepath.Join(DataDir(), "rulesets") }
func Diag() string     { return filepath.Join(DataDir(), "diag") }

// UIPrefs 面板偏好(语言、外观);Windows 客户端另有每用户的 ui.json,这个给 Linux 的 Web 面板用。
func UIPrefs() string { return filepath.Join(ConfDir(), "ui.json") }

// Ensure 建好设置目录、数据目录与子目录。建不出来是致命的。
func Ensure() error {
	for _, d := range []string{ConfDir(), DataDir(), Logs(), RuleSets(), filepath.Join(DataDir(), "profiles")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Harden 把数据目录的权限收紧到"只有管理员与系统能进"(Windows 上才有实际动作,见 secure_windows.go)。
//
// **每次启动都做一遍**,不是只在首次建目录时:老版本装出来的目录、手工建的目录、被别的程序改过权限的,
// 都得在这一次修回来,否则老用户永远修不上。
//
// 收不动不算致命(便携方式或非管理员身份跑的时候本来就收不动),调用方记一行日志就行。
func Harden() error { return secureDataDir(DataDir()) }
