package daemon

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// crashInNativeCode 在非 Go 代码里访问违例(往地址 8 写零),模拟 wintun / WFP 这类 DLL 崩掉。
func crashInNativeCode() {
	_, _, _ = windows.NewLazySystemDLL("ntdll.dll").NewProc("RtlZeroMemory").Call(8, 16)
}

// Windows 服务模式:标准错误整个接到 crash.log。panic 只记一遍(不能标准错误一份、崩溃副本又一份)。
func TestCaptureCrashesServicePanic(t *testing.T) {
	s := runCrashChild(t, "panic", true)
	mustContain(t, s, "panic: 故意崩溃:crash.log 测试", "goroutine")
	// 两路一起写同一个文件时是逐段交错的("panic: panic: 故意崩溃…故意崩溃…"),光数"panic: 故意崩溃"数不出来;
	// 数整句崩溃消息,并且不许出现"goroutine goroutine"这种交错
	if n := strings.Count(s, "故意崩溃:crash.log 测试"); n != 1 || strings.Contains(s, "goroutine goroutine") {
		t.Fatalf("panic 应当只记一遍、不交错,消息出现了 %d 次:\n%s", n, s)
	}
}

// fatal error 的原因行也要在(只靠 SetCrashOutput 时没有)。
func TestCaptureCrashesServiceFatalHasReason(t *testing.T) {
	mustContain(t, runCrashChild(t, "fatal", true), "fatal error: sync: unlock of unlocked mutex", "goroutine")
}

// DLL 里的访问违例:只靠 SetCrashOutput 时一个字都没有。
func TestCaptureCrashesServiceNativeException(t *testing.T) {
	mustContain(t, runCrashChild(t, "native", true), "Exception 0xc0000005")
}
