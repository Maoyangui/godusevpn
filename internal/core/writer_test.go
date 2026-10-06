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

// 平台写入器拿到的消息带终端颜色码和 sing-box 自己的 "INFO[0055] " 前缀:落盘前去掉,只留 "[连接号 耗时] 标签: 内容"。
func TestCleanMessage(t *testing.T) {
	cases := map[string]string{
		"\x1b[36mINFO\x1b[0m[55681] [\x1b[38;5;146m525861506\x1b[0m 3ms] outbound/hysteria2[西班牙3 x2]: outbound connection to 172.217.112.4:443": "[525861506 3ms] outbound/hysteria2[西班牙3 x2]: outbound connection to 172.217.112.4:443",
		"\x1b[31mERROR\x1b[0m[0012] dns: exchange failed":   "dns: exchange failed",
		"WARN[0003] router: something":                      "router: something",
		"[123 1ms] inbound/tun[tun-in]: inbound connection": "[123 1ms] inbound/tun[tun-in]: inbound connection",
		"已经干净的一行":                                           "已经干净的一行",
		"INFO[x] 不是秒数的不动":                                   "INFO[x] 不是秒数的不动",
		"\x1b[1m":                                           "",
	}
	for in, want := range cases {
		if got := CleanMessage(in); got != want {
			t.Errorf("CleanMessage(%q) = %q,应为 %q", in, got, want)
		}
	}
	var got string
	Writer{Printf: func(level, format string, a ...any) { got = a[0].(string) }}.WriteMessage(log.LevelInfo, "\x1b[36mINFO\x1b[0m[0001] dns: x")
	if got != "dns: x" {
		t.Fatalf("写入器落盘前应清理消息,得到 %q", got)
	}
}
