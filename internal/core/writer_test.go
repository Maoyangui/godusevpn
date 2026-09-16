package core

import (
	"sync/atomic"
	"testing"

	"github.com/sagernet/sing-box/log"
)

// sing-box 把每一级的消息都交给平台写入器;设置 info 时 debug 不能落盘,warn / error 要落。
func TestWriterFiltersByLevel(t *testing.T) {
	var got []string
	var lv atomic.Int32
	lv.Store(LevelOf("info"))
	w := Writer{Printf: func(level, format string, a ...any) { got = append(got, level) }, Level: &lv}
	w.WriteMessage(log.LevelDebug, "d")
	w.WriteMessage(log.LevelTrace, "t")
	w.WriteMessage(log.LevelInfo, "i")
	w.WriteMessage(log.LevelWarn, "w")
	w.WriteMessage(log.LevelError, "e")
	if len(got) != 3 || got[0] != "INFO" || got[1] != "WARN" || got[2] != "ERROR" {
		t.Fatalf("info 级别应只留 info/warn/error,得到 %v", got)
	}
	lv.Store(LevelOf("debug"))
	w.WriteMessage(log.LevelDebug, "d2")
	if len(got) != 4 {
		t.Fatalf("调到 debug 后 debug 应落盘: %v", got)
	}
	if LevelOf("nonsense") != int32(log.LevelInfo) {
		t.Fatal("认不出的级别名应按 info")
	}
	// Level 为 nil 不过滤
	n := 0
	Writer{Printf: func(string, string, ...any) { n++ }}.WriteMessage(log.LevelTrace, "x")
	if n != 1 {
		t.Fatal("没设 Level 时不该过滤")
	}
}
