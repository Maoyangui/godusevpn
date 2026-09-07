// Package paths 客户端的文件位置。服务的一切都在 %ProgramData%\godusevpn 下:
// 订阅缓存、生成的配置、fake-ip 缓存、日志。目录只给管理员与 SYSTEM 读写(安装时设 ACL),
// 因为配置里有节点凭据。
package paths

import (
	"os"
	"path/filepath"
)

const AppName = "godusevpn"

// DataDir 服务数据目录。
func DataDir() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, AppName)
}

func Settings() string { return filepath.Join(DataDir(), "settings.json") }

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

// Ensure 建好数据目录与子目录。
func Ensure() error {
	for _, d := range []string{DataDir(), Logs(), RuleSets(), filepath.Join(DataDir(), "profiles")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}
