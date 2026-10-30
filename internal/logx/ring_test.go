package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRingKeepsNewest(t *testing.T) {
	var r Ring
	if got := r.Tail(5); got != nil {
		t.Fatalf("空的应返回空: %v", got)
	}
	r.Add("a")
	r.Add("b")
	if got := r.Tail(5); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("没满: %v", got)
	}
	for i := 0; i < ringLines+3; i++ {
		r.Add(fmt.Sprint(i))
	}
	got := r.Tail(ringLines + 100)
	if len(got) != ringLines || got[0] != "3" || got[len(got)-1] != fmt.Sprint(ringLines+2) {
		t.Fatalf("满了之后: 共 %d 行,首 %s 尾 %s", len(got), got[0], got[len(got)-1])
	}
	if got := r.Tail(2); !reflect.DeepEqual(got, []string{fmt.Sprint(ringLines + 1), fmt.Sprint(ringLines + 2)}) {
		t.Fatalf("取最后两行: %v", got)
	}
}

func TestLevelOf(t *testing.T) {
	if got := LevelOf(Line("WARN", "x %d", 1)); got != "WARN" {
		t.Fatalf("实际 %q", got)
	}
	if got := LevelOf("short"); got != "" {
		t.Fatalf("格式不对应返回空: %q", got)
	}
	if Serious("INFO") || Serious("DEBUG") || Serious("") || !Serious("ERROR") {
		t.Fatal("Serious 判断不对")
	}
}

func TestFilterRewritesAndKeepsMtime(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "core.log")
	r := New(p, 1<<20, 2)
	r.Printf("INFO", "visited a.example.com")
	r.Printf("ERROR", "failed b.example.com")
	if err := os.WriteFile(p+".1", []byte("2026-10-01 00:00:00 INFO old\n2026-10-01 00:00:01 WARN keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(p+".1", old, old); err != nil {
		t.Fatal(err)
	}
	keep := func(l string) (string, bool) {
		if !Serious(LevelOf(l)) {
			return "", false
		}
		return strings.ReplaceAll(l, "b.example.com", "*.com"), true
	}
	r.Filter(keep)
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "a.example.com") || !strings.Contains(string(b), "failed *.com") {
		t.Fatalf("当前文件: %s", b)
	}
	b, _ = os.ReadFile(p + ".1")
	if string(b) != "2026-10-01 00:00:01 WARN keep\n" {
		t.Fatalf("滚动出去的那份: %s", b)
	}
	if st, _ := os.Stat(p + ".1"); !st.ModTime().Equal(old) {
		t.Fatalf("修改时间应照旧: %v", st.ModTime())
	}
	// Filter 关掉了开着的文件,之后还能接着写
	r.Printf("WARN", "after")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if b, _ = os.ReadFile(p); !strings.Contains(string(b), "WARN after") || !strings.Contains(string(b), "failed *.com") {
		t.Fatalf("过滤之后接着写: %s", b)
	}
}
