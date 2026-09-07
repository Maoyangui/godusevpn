// Package ipc 托盘客户端 / 命令行 ↔ 服务 的本机控制协议:命名管道上一行一个 JSON。
// 每次调用一条连接(连、发、收、断),没有长连接状态要维护;事件推送后面用另一条订阅连接做。
package ipc

import (
	"encoding/json"
	"fmt"
)

// PipeName 管道名。ACL:SYSTEM 与管理员完全控制,本机已登录用户可读写(见 pipe_windows.go)。
const PipeName = `\\.\pipe\godusevpn`

// Version 协议版本:客户端与服务不一致时提示升级。
const Version = 1

// 方法名
const (
	MGetState       = "GetState"
	MConnect        = "Connect"
	MDisconnect     = "Disconnect"
	MSetMode        = "SetMode"
	MSelectNode     = "SelectNode"
	MTestLatency    = "TestLatency"
	MGetProfile     = "GetProfile"
	MSetProfileURL  = "SetProfileURL"
	MRefreshProfile = "RefreshProfile"
	MGetSettings    = "GetSettings"
	MSetSettings    = "SetSettings"
	MGetLogs        = "GetLogs"
	MDiagnose       = "Diagnose"
	MPing           = "Ping"
	MGetClashInfo   = "GetClashInfo" // 内核 Clash API 的端口与密钥,托盘客户端据此读实时速度、连接与节点延迟
	MExportDiag     = "ExportDiag"   // 生成脱敏的诊断 zip,返回路径
	MGetProfiles    = "GetProfiles"  // 订阅列表
	MAddProfile     = "AddProfile"   // 新增订阅(先拉一次验证)
	MRemoveProfile  = "RemoveProfile"
	MSelectProfile  = "SelectProfile" // 切换当前订阅
	MRenameProfile  = "RenameProfile"
)

type Request struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	ID     int64           `json:"id"`
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Code   string          `json:"code,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// CallError 服务端返回的失败。
type CallError struct {
	Code string
	Msg  string
}

func (e *CallError) Error() string {
	if e.Code != "" {
		return e.Code + ": " + e.Msg
	}
	return e.Msg
}

// Handler 一个方法的实现:入参 JSON,出参任意可序列化的值。
type Handler func(params json.RawMessage) (any, error)

// Decode 解入参的小助手。
func Decode[T any](params json.RawMessage) (T, error) {
	var v T
	if len(params) == 0 {
		return v, nil
	}
	if err := json.Unmarshal(params, &v); err != nil {
		return v, fmt.Errorf("参数无效: %w", err)
	}
	return v, nil
}
