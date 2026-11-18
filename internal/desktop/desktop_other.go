//go:build !linux

// Package desktop 只有 Linux 需要(Windows / macOS 的图形界面自己登记 godusevpn:// 协议);别的平台都是空的。
package desktop

import "errors"

func Available() bool                      { return false }
func Install(string, []byte) (bool, error) { return false, nil }
func Uninstall()                           {}
func WriteHint(string)                     {}
func PanelURL() string                     { return "" }
func Alive(string) bool                    { return false }
func Open(string) error                    { return errors.New("这个平台不经这里打开面板") }
func Notify(string, string)                {}
