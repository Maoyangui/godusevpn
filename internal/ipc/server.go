package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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
}

func NewServer(logf func(string, ...any)) *Server {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Server{handlers: map[string]Handler{}, logf: logf}
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

func (s *Server) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
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
		w.Write(b)
		w.WriteByte('\n')
		if err := w.Flush(); err != nil {
			return
		}
		_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
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
	ErrNoPermission = errors.New("没有权限连接控制口(需要 root / sudo)")
)

func Call(ctx context.Context, method string, params any, result any) error {
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := dial(dctx)
	if err != nil {
		if permissionDenied(err) {
			return ErrNoPermission
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
