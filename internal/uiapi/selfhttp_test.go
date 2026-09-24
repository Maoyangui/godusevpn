package uiapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

type plainBackend struct{}

func (plainBackend) Dispatch(string, json.RawMessage) (any, error) { return nil, nil }
func (plainBackend) Logf(string, ...any)                           {}

type sealedBackend struct{ plainBackend }

var errTestSealed = errors.New("闸开着、隧道没起来")

func (sealedBackend) SelfHTTP(time.Duration) (*http.Client, error) { return nil, errTestSealed }

// viaBackend 给出一个客户端:它一发请求就报 errTestUsed,用来确认更新检查真的用了后端给的客户端。
type viaBackend struct{ plainBackend }

var errTestUsed = errors.New("用了后端给的客户端")

type failRT struct{}

func (failRT) RoundTrip(*http.Request) (*http.Response, error) { return nil, errTestUsed }

func (viaBackend) SelfHTTP(time.Duration) (*http.Client, error) {
	return &http.Client{Transport: failRT{}}, nil
}

// 更新检查 / 下载安装包用后端给的客户端:Linux / macOS 的守护进程是 root,闸放行它,默认客户端直连出去就是
// 隧道外流量。后端说"现在不许出去",就不出去;后端没有这个接口(比如只给界面用的后端),行为不变。
func TestUpdateCheckUsesBackendClient(t *testing.T) {
	s := New(sealedBackend{}, Options{})
	if _, err := s.checkUpdate(true); !errors.Is(err, errTestSealed) {
		t.Fatalf("后端不给客户端时更新检查应当直接报它的错、不去联网,得到 %v", err)
	}
	if _, err := New(viaBackend{}, Options{}).checkUpdate(true); err == nil || !strings.Contains(err.Error(), errTestUsed.Error()) {
		t.Fatalf("后端给了客户端,更新检查却没用它:%v", err)
	}
	if c, err := New(plainBackend{}, Options{}).httpClient(time.Second); c != nil || err != nil {
		t.Fatalf("后端没有 SelfHTTP 时应当用各函数自己的默认值:c=%v err=%v", c, err)
	}
}
