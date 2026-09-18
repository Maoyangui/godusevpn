package core

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// 内核没跑时的测速:只用出站起临时实例;连不上的节点要在父上下文超时内给出 -1,不能挂死也不能 panic。
func TestProbeOfflineUnreachable(t *testing.T) {
	outbounds := []json.RawMessage{
		json.RawMessage(`{"type":"hysteria2","tag":"hk","server":"192.0.2.1","server_port":443,"password":"p","tls":{"enabled":true,"server_name":"a.example"}}`),
		json.RawMessage(`{"type":"anytls","tag":"tw","server":"192.0.2.2","server_port":8443,"password":"p","tls":{"enabled":true,"server_name":"b.example"}}`),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	res, err := Probe(ctx, outbounds, []string{"hk", "tw", "不存在"}, "", nil)
	if err != nil {
		t.Fatalf("这几个节点内核都解得动,不该报错: %v", err)
	}
	if time.Since(start) > 6*time.Second {
		t.Fatal("超时后应尽快返回")
	}
	if len(res) != 2 || res["hk"] != -1 || res["tw"] != -1 {
		t.Fatalf("连不上的节点应为 -1,不存在的节点不出现: %v", res)
	}
}

// 临时测速实例绝不能往进程当前目录写文件。
// sing-box 的 needCacheFile 是 `CacheFile.Enabled || PlatformLogWriter != nil`,而这份临时配置里
// 没有 experimental 段 —— 只要传了 PlatformLogWriter,缓存文件就会落到默认路径 "cache.db",
// 那是相对**进程当前目录**的。Android 上当前目录是 /,写不进去,box.Start() 直接失败,
// 未连接时测速一个数都出不来,而且是完全静默的;Windows 上服务以 SYSTEM 跑,会往 System32 里丢文件。
func TestProbeWritesNothingToWorkingDir(t *testing.T) {
	dir := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(old) }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = Probe(ctx, []json.RawMessage{
		json.RawMessage(`{"type":"anytls","tag":"tw","server":"192.0.2.2","server_port":8443,"password":"p","tls":{"enabled":true,"server_name":"b.example"}}`),
	}, []string{"tw"}, "", nil)

	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		t.Fatalf("测速在当前目录留下了 %s —— Android 上这一步会直接失败,所有节点都测不出来", e.Name())
	}
}

// 订阅里有内核解不动的节点时,不能把整轮测速一起拖垮:坏的那个报 -1,别的照测。
func TestProbeSkipsUnparsableOutbound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	res, err := Probe(ctx, []json.RawMessage{
		json.RawMessage(`{"type":"anytls","tag":"好的","server":"192.0.2.2","server_port":8443,"password":"p","tls":{"enabled":true,"server_name":"b.example"}}`),
		json.RawMessage(`{"type":"这个类型内核不认识","tag":"坏的","server":"192.0.2.3","server_port":443}`),
	}, []string{"好的", "坏的"}, "", nil)
	if err == nil {
		t.Fatal("跳过了坏节点,应该把这件事报上来")
	}
	if _, ok := res["好的"]; !ok {
		t.Fatalf("好的节点应该照测出来: %v", res)
	}
	if res["坏的"] != -1 {
		t.Fatalf("解不动的节点该报 -1,而不是在界面上留白让人以为没测过: %v", res)
	}
}
