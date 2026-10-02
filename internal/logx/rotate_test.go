package logx

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRotateByAge(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.log")
	old := time.Now().Add(-48*time.Hour).Format(stamp) + " INFO 很久以前\n"
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(p, 1<<20, 3)
	r.SetMaxAge(24 * time.Hour)
	r.Printf("INFO", "现在")
	r.Close()
	if _, err := os.Stat(p + ".1"); err != nil {
		t.Fatal("超过保留时间的文件应被滚成 .1")
	}
	b, _ := os.ReadFile(p)
	if len(Tail(p, 10)) != 1 || string(b[:0]) != "" || !contains(string(b), "现在") || contains(string(b), "很久以前") {
		t.Fatalf("新文件里只该有新行: %q", b)
	}

	// 不设保留时间就不滚
	p2 := filepath.Join(dir, "b.log")
	_ = os.WriteFile(p2, []byte(old), 0o600)
	r2 := New(p2, 1<<20, 3)
	r2.Printf("INFO", "现在")
	r2.Close()
	if _, err := os.Stat(p2 + ".1"); err == nil {
		t.Fatal("maxAge 为 0 时不该按时间滚动")
	}
}

func TestPrune(t *testing.T) {
	dir := t.TempDir()
	oldT := time.Now().Add(-10 * 24 * time.Hour)
	for _, n := range []string{"service.log", "service.log.1", "service.log.2", "diag.zip"} {
		p := filepath.Join(dir, n)
		_ = os.WriteFile(p, []byte("x"), 0o600)
		_ = os.Chtimes(p, oldT, oldT)
	}
	_ = os.WriteFile(filepath.Join(dir, "core.log.1"), []byte("x"), 0o600) // 新的
	if n := Prune(dir, 7*24*time.Hour); n != 3 {
		t.Fatalf("应删 3 个旧文件,实际 %d", n)
	}
	for _, n := range []string{"service.log", "core.log.1"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatalf("%s 不该被删", n)
		}
	}
	if Prune(dir, 0) != 0 {
		t.Fatal("maxAge 0 不删任何东西")
	}
}

// 写进日志的每一行都先打码:拉订阅失败的错误原样带着完整订阅地址,日志页、日志目录、诊断包都会给人看。
func TestPrintfRedacts(t *testing.T) {
	p := filepath.Join(t.TempDir(), "service.log")
	r := New(p, 1<<20, 3)
	r.Printf("INFO", "订阅经 %s 拉取失败: %v", "proxy", `拉取订阅失败: Get "https://panel.example:2056/sub/SECRETTOKEN?format=json": timeout`)
	r.Close()
	b, _ := os.ReadFile(p)
	if contains(string(b), "SECRETTOKEN") || !contains(string(b), `Get "https://panel.example:2056/***": timeout`) {
		t.Fatalf("日志里的订阅地址没打码: %s", b)
	}
}

// 老版本写下、没打过码的几份:第一次打开时就地打码;不含地址的行一个字节不动,修改时间照旧(按时间清理靠它)。
func TestScrubsOldLogs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "service.log")
	leak := "2026-09-21 10:00:00 INFO 订阅经 proxy 拉取失败: 拉取订阅失败: Get \"https://panel.example:2056/sub/SECRETTOKEN?format=json\": timeout\n"
	plain := "2026-09-21 10:00:01 INFO 状态 → connected\n"
	oldT := time.Now().Add(-3 * 24 * time.Hour).Truncate(time.Second)
	for _, f := range []string{p, p + ".2"} {
		if err := os.WriteFile(f, []byte(leak+plain), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(f, oldT, oldT)
	}
	cleanOld := p + ".1"
	_ = os.WriteFile(cleanOld, []byte(plain), 0o600)
	_ = os.Chtimes(cleanOld, oldT, oldT)

	r := New(p, 1<<20, 3)
	r.Printf("INFO", "启动")
	r.Close()
	for _, f := range []string{p, p + ".1", p + ".2"} {
		b, _ := os.ReadFile(f)
		if contains(string(b), "SECRETTOKEN") || !contains(string(b), plain) {
			t.Fatalf("%s 没打码或误伤了别的行:\n%s", f, b)
		}
	}
	for _, f := range []string{p + ".1", p + ".2"} {
		if st, _ := os.Stat(f); !st.ModTime().Equal(oldT) {
			t.Fatalf("%s 的修改时间被改了: %v", f, st.ModTime())
		}
	}
	if _, err := os.Stat(p + ".2.tmp"); err == nil {
		t.Fatal("临时文件没清掉")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
