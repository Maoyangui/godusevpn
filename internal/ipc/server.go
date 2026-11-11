package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Server 控制口服务端:Windows 上是命名管道,Linux 上是 Unix socket(见 listen_*.go),协议一样。
type Server struct {
	mu       sync.RWMutex
	handlers map[string]Handler
	ln       net.Listener
	logf     func(string, ...any)
	idle     time.Duration // 见 idleTimeout;Listen 之前定好,之后只读
}

func NewServer(logf func(string, ...any)) *Server {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Server{handlers: map[string]Handler{}, logf: logf, idle: idleTimeout}
}

func (s *Server) Handle(method string, h Handler) {
	s.mu.Lock()
	s.handlers[method] = h
	s.mu.Unlock()
}

// Listen 开始监听;返回后在后台接受连接,直到 Close。
func (s *Server) Listen() error {
	ln, err := listen()
	if err != nil {
		return err
	}
	s.ln = ln
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				if listenerClosed(err) {
					return
				}
				s.logf("控制口 accept: %v", err)
				continue
			}
			go s.serve(conn)
		}
	}()
	return nil
}

func (s *Server) Close() error {
	if s.ln != nil {
		return s.ln.Close()
	}
	return nil
}

// idleTimeout 连接上等下一条请求、写回一条应答各最多这么久。处理器本身跑多久不在这里限:换新配置的方法要好几分钟
// (见 SlowCallTimeout),以前整条连接只给 60 秒,处理器还没跑完连接就到期,应答写不回去,调用方只能等到自己超时。
const idleTimeout = 60 * time.Second

func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(s.idle))
	r := bufio.NewReaderSize(conn, 1<<20)
	w := bufio.NewWriter(conn)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req Request
		resp := Response{}
		if err := json.Unmarshal(line, &req); err != nil {
			resp.Error = "请求不是合法 JSON"
		} else {
			resp.ID = req.ID
			_ = conn.SetDeadline(time.Time{}) // 处理器跑多久都行:调用方自己带着等多久(见 Call)
			s.mu.RLock()
			h := s.handlers[req.Method]
			s.mu.RUnlock()
			if h == nil {
				resp.Error = "没有这个方法: " + req.Method
			} else {
				res, err := call(h, req.Params)
				if err != nil {
					resp.Error = err.Error()
					var ce *CallError
					if errors.As(err, &ce) {
						resp.Code, resp.Error = ce.Code, ce.Msg
					}
				} else {
					b, err := json.Marshal(res)
					if err != nil {
						resp.Error = "结果序列化失败: " + err.Error()
					} else {
						resp.OK, resp.Result = true, b
					}
				}
			}
		}
		b, _ := json.Marshal(resp)
		_ = conn.SetDeadline(time.Now().Add(s.idle)) // 写回应答、等下一条请求
		w.Write(b)
		w.WriteByte('\n')
		if err := w.Flush(); err != nil {
			return
		}
		_ = conn.SetDeadline(time.Now().Add(s.idle))
	}
}

// Dispatch 在服务内部直接调一个已注册的方法(一个接口复用另一个接口的实现;Web 面板也走这里)。
func (s *Server) Dispatch(method string, params json.RawMessage) (any, error) {
	s.mu.RLock()
	h := s.handlers[method]
	s.mu.RUnlock()
	if h == nil {
		return nil, errors.New("没有这个方法: " + method)
	}
	return call(h, params)
}

// Methods 已注册的方法名(Web 面板据此判断哪些直接转发)。
func (s *Server) Methods() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := make(map[string]bool, len(s.handlers))
	for k := range s.handlers {
		m[k] = true
	}
	return m
}

// call 兜住处理器的 panic,别让一个坏请求把服务带走。
func call(h Handler, params json.RawMessage) (res any, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = errors.New("服务内部错误")
		}
	}()
	return h(params)
}

// Call 客户端:连上控制口、发一条、收一条。服务没起来时返回 ErrNoService;没权限连(Linux 上非 root)返回 ErrNoPermission。
var (
	ErrNoService    = errors.New("服务未运行")
	ErrNoPermission = errors.New("没有权限连接控制口")
	// permissionHint 各平台在 init 里填:Linux / macOS 是"需要 root / sudo",Windows 是"账户没登记"。
	permissionHint = "需要 root / sudo"
	// errImpostor 控制口对面不是佛跳墙服务(Windows 上有人抢注了管道名,见 verifyPipeServer)。
	errImpostor = errors.New("控制口被别的程序占着,不是佛跳墙服务;重启电脑后再试")
)

// SlowCallTimeout 界面、命令行等一次控制调用最多等多久。要不短于守护进程里最长的那种同步处理:
// 连着时拉订阅的整条回退链(当前节点、强测自动选择组、自动选择三次、直连,约 5 分 40 秒,见 daemon.fetchChainBudget)
// 之后再换新配置(备配置最多 state.PrepareTimeout,再停旧起新)。以前桌面端只等 90 秒、命令行 60 秒:
// 新配置还在备,界面先报超时,过一会儿又自己连好了。
//
// 取舍:不改成"先返回、做完再推送" —— 那要三个前端(桌面、网页面板、Android)都改成按推送收结果,
// 守护进程这些处理本身都有上限,等得起;代价是守护进程真卡住时界面要等满这么久才报错(别的按钮照常能点,断开另走一条连接)。
const SlowCallTimeout = 10 * time.Minute

func Call(ctx context.Context, method string, params any, result any) error {
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := dial(dctx)
	if err != nil {
		if permissionDenied(err) {
			return fmt.Errorf("%w(%s)", ErrNoPermission, permissionHint)
		}
		if errors.Is(err, errImpostor) {
			return fmt.Errorf("%w(%v)", ErrNoService, err) // 对调用方仍是"服务不在":恢复网络照样走离线那条路
		}
		return ErrNoService
	}
	defer conn.Close()
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	_ = conn.SetDeadline(deadline)
	req := Request{ID: time.Now().UnixNano(), Method: method}
	if params != nil {
		if req.Params, err = json.Marshal(params); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(req)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return err
	}
	line, err := bufio.NewReaderSize(conn, 1<<20).ReadBytes('\n')
	if err != nil {
		return err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return &CallError{Code: resp.Code, Msg: resp.Error}
	}
	if result != nil && len(resp.Result) > 0 {
		return json.Unmarshal(resp.Result, result)
	}
	return nil
}
