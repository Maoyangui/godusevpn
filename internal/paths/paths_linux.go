//go:build !windows

// Package paths 各平台的文件位置。数据布局三端一致:
// settings.json、profiles/<id>.json、config.json、config.last-good.json、state.json、cache.db、logs/、rulesets/、diag/。
//
// Linux:设置在 /etc/godusevpn,数据在 /var/lib/godusevpn;梅林等 Entware 环境改到 /opt 下;
// 环境变量 GODUSEVPN_CONF / GODUSEVPN_DATA 可整体覆盖(测试与非 root 运行用)。
package paths

import (
	"os"
	"path/filepath"
)

const AppName = "godusevpn"

// entware 梅林 / 部分 padavan 固件的软件环境:根文件系统只读,一切装在 /opt。
func entware() bool {
	if _, err := os.Stat("/opt/etc/entware_release"); err == nil {
		return true
	}
	_, err := os.Stat("/opt/bin/opkg")
	return err == nil && !fileExists("/etc/openwrt_release")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// DataDir 数据目录:缓存、规则集、日志、诊断包。
func DataDir() string {
	if p := os.Getenv("GODUSEVPN_DATA"); p != "" {
		return p
	}
	if entware() {
		return "/opt/var/lib/" + AppName
	}
	return "/var/lib/" + AppName
}

// ConfDir 设置文件目录。
func ConfDir() string {
	if p := os.Getenv("GODUSEVPN_CONF"); p != "" {
		return p
	}
	if entware() {
		return "/opt/etc/" + AppName
	}
	return filepath.Join("/etc", AppName)
}
