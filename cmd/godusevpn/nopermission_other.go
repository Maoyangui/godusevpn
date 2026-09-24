//go:build !windows && !darwin

package main

import "errors"

// noPermissionHint Linux:连接控制口要 root / sudo。
func noPermissionHint() (string, bool) { return "root", false }

func registerController(string) error { return errors.New("E_NO_PERMISSION: ") }
