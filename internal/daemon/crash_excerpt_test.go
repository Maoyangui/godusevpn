package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCrashLog(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "crash.log")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func header(pid int) string {
	return fmt.Sprintf("== 2026-09-25 00:00:%02d.000 启动 v0.7.5 pid=%d\n", pid%60, pid)
}

// 每段的原因行在最前面,后面跟着上千行栈。只取文件最后几百行会把原因截掉。
func TestCrashExcerptKeepsReasonLines(t *testing.T) {
	var b strings.Builder
	b.WriteString(header(1))
	b.WriteString("fatal error: concurrent map writes\n\n")
	for j := 0; j < 1000; j++ {
		fmt.Fprintf(&b, "goroutine %d [running]:\n", j)
	}
	b.WriteString(header(2))
	s := crashExcerpt(writeCrashLog(t, b.String()))
	for _, want := range []string{"fatal error: concurrent map writes", "pid=1", "省略", "goroutine 999 [", "之后又启动了 1 次", "pid=2"} {
		if !strings.Contains(s, want) {
			t.Fatalf("摘要里没有 %q:\n%s", want, s)
		}
	}
	if n := strings.Count(s, "\n"); n > 210 {
		t.Fatalf("摘要太长(%d 行)", n)
	}
}

// 崩溃之后服务被自动拉起、又开了几次机:每次启动都写一行抬头。按段数取的话崩溃那段会被挤出去。
func TestCrashExcerptSkipsHeaderOnlyStarts(t *testing.T) {
	var b strings.Builder
	b.WriteString(header(100))
	b.WriteString("panic: runtime error: invalid memory address or nil pointer dereference\n")
	b.WriteString("goroutine 7 [running]:\nmain.f()\n")
	for pid := 200; pid < 205; pid++ {
		b.WriteString(header(pid))
	}
	s := crashExcerpt(writeCrashLog(t, b.String()))
	for _, want := range []string{"panic: runtime error", "pid=100", "之后又启动了 5 次", "pid=204"} {
		if !strings.Contains(s, want) {
			t.Fatalf("摘要里没有 %q:\n%s", want, s)
		}
	}
}

// 有内容的段超过 3 个时只取最后 3 个。
func TestCrashExcerptLastThreeCrashes(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 5; i++ {
		b.WriteString(header(i))
		fmt.Fprintf(&b, "Exception 0xc0000005 第%d次\n", i)
	}
	s := crashExcerpt(writeCrashLog(t, b.String()))
	for _, want := range []string{"第3次", "第4次", "第5次"} {
		if !strings.Contains(s, want) {
			t.Fatalf("摘要里没有 %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "第1次") || strings.Contains(s, "第2次") || strings.Contains(s, "之后又启动") {
		t.Fatalf("只该取最后 3 段、最后一段就是崩溃段:\n%s", s)
	}
}

// 全是启动抬头:没有崩溃记录,留最后一行抬头说明记录是开着的。
func TestCrashExcerptOnlyHeaders(t *testing.T) {
	s := crashExcerpt(writeCrashLog(t, header(1)+header(2)+"\n"+header(3)))
	if !strings.Contains(s, "pid=3") || !strings.Contains(s, "没有崩溃记录") || strings.Contains(s, "pid=1") {
		t.Fatalf("全是抬头时应当只给最后一行抬头和一句说明:\n%s", s)
	}
}

// 没有抬头的(Android 那份)照旧取最后 300 行;空文件 / 不存在不出摘要。
func TestCrashExcerptWithoutHeaders(t *testing.T) {
	var b strings.Builder
	for j := 0; j < 1000; j++ {
		fmt.Fprintf(&b, "line %d\n", j)
	}
	p := writeCrashLog(t, b.String())
	s := crashExcerpt(p)
	if !strings.Contains(s, "line 999") || strings.Contains(s, "line 600\n") {
		t.Fatalf("没有抬头时应当只取最后 300 行:%d 行", strings.Count(s, "\n"))
	}
	dir := t.TempDir()
	if crashExcerpt(filepath.Join(dir, "none.log")) != "" {
		t.Fatal("文件不存在不该出摘要")
	}
	empty := filepath.Join(dir, "empty.log")
	_ = os.WriteFile(empty, nil, 0o600)
	if crashExcerpt(empty) != "" {
		t.Fatal("空文件不该出摘要")
	}
}

// 轮转时被截在中间的那段(第一个抬头之前的内容)也算一段,不能丢。
func TestCrashExcerptKeepsPreamble(t *testing.T) {
	s := crashExcerpt(writeCrashLog(t, "Exception 0xc0000005 0x1 0x8\nPC=0x7ff\n"+header(9)))
	if !strings.Contains(s, "Exception 0xc0000005") || !strings.Contains(s, "pid=9") {
		t.Fatalf("第一个抬头之前的内容丢了:\n%s", s)
	}
}
