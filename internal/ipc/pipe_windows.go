package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
)

// sddl:SYSTEM 与管理员完全控制,已登录的本机用户可读写(能控制连接、改设置);其他人连不上。
const sddl = "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;AU)"

// Server 管道服务端。
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
	ln, err := winio.ListenPipe(PipeName, &winio.PipeConfig{SecurityDescriptor: sddl})
	if err != nil {
		return err
	}
	s.ln = ln
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				if errors.Is(err, winio.ErrPipeListenerClosed) {
					return
				}
				s.logf("管道 accept: %v", err)
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

// call 兜住处理器的 panic,别让一个坏请求把服务带走。
func call(h Handler, params json.RawMessage) (res any, err error) {
	defer func() {
		if v := recover(); v != nil {
			err = errors.New("服务内部错误")
		}
	}()
	return h(params)
}

// Call 客户端:连管道、发一条、收一条。服务没起来时返回 ErrNoService。
var ErrNoService = errors.New("服务未运行")

func Call(ctx context.Context, method string, params any, result any) error {
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	conn, err := winio.DialPipeContext(dctx, PipeName)
	if err != nil {
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
