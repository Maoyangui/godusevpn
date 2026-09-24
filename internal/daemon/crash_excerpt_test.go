package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 诊断包里的崩溃摘要:每段的原因行在最前面,后面跟着上千行栈。只取文件最后几百行会把原因截掉。
func TestCrashExcerptKeepsReasonLines(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&b, "== 2026-09-25 0%d:00:00.000 启动 v0.7.5 pid=%d\n", i, i)
		if i == 4 { // 倒数第二段:一次 fatal error,带 1000 行栈
			b.WriteString("fatal error: concurrent map writes\n\n")
			for j := 0; j < 1000; j++ {
				fmt.Fprintf(&b, "goroutine %d [running]:\n", j)
			}
		}
	}
	p := filepath.Join(t.TempDir(), "crash.log")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	s := crashExcerpt(p)
	for _, want := range []string{"fatal error: concurrent map writes", "pid=3", "pid=4", "pid=5", "省略", "goroutine 999 ["} {
		if !strings.Contains(s, want) {
			t.Fatalf("摘要里没有 %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "pid=1\n") || strings.Contains(s, "pid=2\n") {
		t.Fatalf("只该取最后 3 段:\n%s", s)
	}
	if n := strings.Count(s, "\n"); n > 3*200+10 {
		t.Fatalf("摘要太长(%d 行)", n)
	}
}

// 没有抬头的(Android 那份)照旧取最后 300 行;空文件 / 不存在不出摘要。
func TestCrashExcerptWithoutHeaders(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for j := 0; j < 1000; j++ {
		fmt.Fprintf(&b, "line %d\n", j)
	}
	p := filepath.Join(dir, "crash.log")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	s := crashExcerpt(p)
	if !strings.Contains(s, "line 999") || strings.Contains(s, "line 600\n") {
		t.Fatalf("没有抬头时应当只取最后 300 行:%d 行", strings.Count(s, "\n"))
	}
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
	p := filepath.Join(t.TempDir(), "crash.log.1")
	if err := os.WriteFile(p, []byte("Exception 0xc0000005 0x1 0x8\nPC=0x7ff\n== 2026-09-25 启动 v0.7.5 pid=9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := crashExcerpt(p); !strings.Contains(s, "Exception 0xc0000005") || !strings.Contains(s, "pid=9") {
		t.Fatalf("第一个抬头之前的内容丢了:\n%s", s)
	}
}
