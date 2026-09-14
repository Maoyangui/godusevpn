//go:build windows

/* SPDX-License-Identifier: MIT
 *
 * Copyright (C) 2019-2021 WireGuard LLC. All Rights Reserved.
 */

package wfp

// 改自 wireguard-windows tunnel/firewall/helpers.go(MIT)。

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func runTransaction(session uintptr, operation wfpObjectInstaller) error {
	err := fwpmTransactionBegin0(session, 0)
	if err != nil {
		return wrapErr(err)
	}

	err = operation(session)
	if err != nil {
		fwpmTransactionAbort0(session)
		return wrapErr(err)
	}

	err = fwpmTransactionCommit0(session)
	if err != nil {
		fwpmTransactionAbort0(session)
		return wrapErr(err)
	}

	return nil
}

func createWtFwpmDisplayData0(name, description string) (*wtFwpmDisplayData0, error) {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, wrapErr(err)
	}

	descriptionPtr, err := windows.UTF16PtrFromString(description)
	if err != nil {
		return nil, wrapErr(err)
	}

	return &wtFwpmDisplayData0{
		name:        namePtr,
		description: descriptionPtr,
	}, nil
}

func filterWeight(weight uint8) wtFwpValue0 {
	return wtFwpValue0{
		_type: cFWP_UINT8,
		value: uintptr(weight),
	}
}

func wrapErr(err error) error {
	if _, ok := err.(syscall.Errno); !ok {
		return err
	}
	_, file, line, ok := runtime.Caller(1)
	if !ok {
		return fmt.Errorf("Firewall error at unknown location: %w", err)
	}
	return fmt.Errorf("Firewall error at %s:%d: %w", file, line, err)
}

func getCurrentProcessAppID() (*wtFwpByteBlob, error) {
	currentFile, err := os.Executable()
	if err != nil {
		return nil, wrapErr(err)
	}

	curFilePtr, err := windows.UTF16PtrFromString(currentFile)
	if err != nil {
		return nil, wrapErr(err)
	}

	var appID *wtFwpByteBlob
	err = fwpmGetAppIdFromFileName0(curFilePtr, unsafe.Pointer(&appID))
	if err != nil {
		return nil, wrapErr(err)
	}
	return appID, nil
}

// appIDFromPath 按 exe 路径取 WFP 的应用标识(getCurrentProcessAppID 的通用版)。
func appIDFromPath(path string) (*wtFwpByteBlob, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, wrapErr(err)
	}
	var appID *wtFwpByteBlob
	if err := fwpmGetAppIdFromFileName0(p, unsafe.Pointer(&appID)); err != nil {
		return nil, wrapErr(err)
	}
	return appID, nil
}
