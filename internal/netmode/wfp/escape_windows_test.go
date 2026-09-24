//go:build windows

package wfp

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

// 交给 fwpuclnt 的输出参数和条件值必须在堆上(堆对象不搬家),不能留在协程栈上:
//   - 输出参数(枚举句柄、条目指针、条数、提供者指针)经 callProc 以 uintptr 传进去。callProc 不带
//     //go:uintptrescapes 时它们留在栈上,调用途中栈一扩容搬家,DLL 就把结果写进已释放的旧栈 —— 写进另一个
//     协程里,把它的指针改成别的值或空指针(v0.7.5 真机验收里服务刚连上就在系统 DLL 里写空指针 +0x48 崩溃)。
//   - 条件值(隧道地址掩码)以 uintptr 挂在 FWP_CONDITION_VALUE0 里,栈一搬家 DLL 读到的是垃圾,闸的放行范围
//     可能被读错。局域网段用的是包级变量(静态内存),不在这里查。
//
// 这件事由编译器的逃逸分析决定,就直接问编译器。
func TestDLLArgumentsLiveOnHeap(t *testing.T) {
	gobin := filepath.Join(runtime.GOROOT(), "bin", "go.exe")
	if _, err := os.Stat(gobin); err != nil {
		if gobin, err = exec.LookPath("go"); err != nil {
			t.Fatalf("找不到 go 命令,没法看逃逸分析: %v", err)
		}
	}
	cmd := exec.Command(gobin, "build", "-gcflags=-m", ".")
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOFLAGS=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build -gcflags=-m 失败: %v\n%s", err, out)
	}
	for _, want := range []string{
		`enum_windows\.go:\d+:\d+: moved to heap: h\b`,
		`enum_windows\.go:\d+:\d+: moved to heap: entries\b`,
		`enum_windows\.go:\d+:\d+: moved to heap: n\b`,
		`enum_windows\.go:\d+:\d+: moved to heap: p\b`,
		`enum_windows\.go:\d+:\d+: moved to heap: tpl\b`,
		`rules_tun\.go:\d+:\d+: &wtFwpV4AddrAndMask\{\.\.\.\} escapes to heap`,
		`rules_tun\.go:\d+:\d+: &wtFwpV6AddrAndMask\{\.\.\.\} escapes to heap`,
	} {
		if !regexp.MustCompile(want).Match(out) {
			t.Errorf("逃逸分析里没有 %q —— 这个值留在了协程栈上,DLL 可能读写到已经搬走的旧栈", want)
		}
	}
}
