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

// Ensure 建好设置目录、数据目录与子目录。
func Ensure() error {
	for _, d := range []string{ConfDir(), DataDir(), Logs(), RuleSets(), filepath.Join(DataDir(), "profiles")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}
