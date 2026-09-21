package core

import (
	"errors"
	"testing"

	quic "github.com/sagernet/quic-go"
	qtls "github.com/sagernet/sing-quic"
)

// 生产路径上 sing-quic 会把 QUIC 错误再包一层(qtls.WrapError);serverReplied 要能穿过它认出对端重置的流。
func TestServerRepliedSeesThroughQUICWrapper(t *testing.T) {
	if !serverReplied(qtls.WrapError(&quic.StreamError{StreamID: 8, Remote: true})) {
		t.Fatal("被 sing-quic 包过一层的对端重置应认出来")
	}
	if serverReplied(qtls.WrapError(&quic.StreamError{StreamID: 8, Remote: false})) {
		t.Fatal("本端取消的流不是对端答复")
	}
	if serverReplied(qtls.WrapError(errors.New("timeout: no recent network activity"))) {
		t.Fatal("空闲超时不是对端答复")
	}
	if !serverReplied(errors.New("remote error: dial tcp: i/o timeout")) {
		t.Fatal("hysteria2 的 remote error 应认出来")
	}
}
