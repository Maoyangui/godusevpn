package core

import (
	"context"
	"errors"
	"testing"

	M "github.com/sagernet/sing/common/metadata"
)

// 开 UDP 会话失败也算这条会话的一次失败(和拨号失败同权);成功不算成功 —— 那只是握手,没有数据回来。
func TestSessionWatchListenPacketFailureCounts(t *testing.T) {
	w, fo, _ := newTestWatch(t, &fakePolicy{})
	fo.setListenErr(errors.New("timeout"))
	if _, err := w.ListenPacket(context.Background(), M.Socksaddr{}); err == nil {
		t.Fatal("假出站已设成失败,不该成功")
	}
	if failsOf(w) != 1 {
		t.Fatalf("开 UDP 会话失败应记一次,记了 %d", failsOf(w))
	}
	fo.setListenErr(nil)
	pc, err := w.ListenPacket(context.Background(), M.Socksaddr{})
	if err != nil {
		t.Fatalf("假出站没设失败,不该出错: %v", err)
	}
	_ = pc.Close()
	if failsOf(w) != 1 {
		t.Fatalf("开 UDP 会话成功不清失败计数(没有数据回来不算活着),实际 %d", failsOf(w))
	}
}
