// Package web Linux 守护进程内置的面板:把 web/dist 里的竖版页面从 HTTP 端出去,
// 页面里的 window.go.main.App.* 由 dist/api.js 映射成 POST /api/<方法>,事件走 SSE;方法实现在 internal/uiapi。
// 面板绑定非回环地址时要密码(设置里的 webPassword),cookie 会话;命令行走 Unix socket 不需要密码。
package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/uiapi"
	"github.com/Maoyangui/godusevpn/web"
)

type Server struct {
	ui   *uiapi.Service
	logf func(string, ...any)
	mu   sync.Mutex

	sessions map[string]time.Time   // 会话 → 到期
	fails    map[string][]time.Time // 登录失败时间(按来源 IP)
}

func New(ui *uiapi.Service, logf func(string, ...any)) *Server {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Server{ui: ui, logf: logf, sessions: map[string]time.Time{}, fails: map[string][]time.Time{}}
}

// Serve 跑到 ctx 结束;设置里的监听地址改了(比如从只听本机改成 0.0.0.0)就换地址重开,不用重启服务。
func (s *Server) Serve(ctx context.Context, listen string) error {
	go s.ui.Run(ctx)
	for ctx.Err() == nil {
		if listen == "" {
			s.logf("面板未启用(设置 webListen 为空)")
		} else if err := s.serveOnce(ctx, listen); err != nil {
			s.logf("面板监听 %s 失败: %v(10 秒后重试)", listen, err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(10 * time.Second):
			}
		}
		for ctx.Err() == nil { // 等地址变化
			cur := strings.TrimSpace(s.ui.Settings().WebListen)
			if cur != listen {
				listen = cur
				break
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(3 * time.Second):
			}
		}
	}
	return nil
}

// serveOnce 在 listen 上服务,直到 ctx 结束或设置里的地址变了才返回。
func (s *Server) serveOnce(ctx context.Context, listen string) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	stop := make(chan struct{})
	go func() {
		defer func() {
			sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-time.After(3 * time.Second):
				if strings.TrimSpace(s.ui.Settings().WebListen) != listen {
					s.logf("面板监听地址已改,重新监听")
					return
				}
			}
		}
	}()
	s.logf("面板监听 http://%s/", listen)
	err = srv.Serve(ln)
	close(stop)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// ---- 事件(SSE) ----

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", 500)
		return
	}
	ch, cancel := s.ui.Subscribe()
	defer cancel()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	write := func(e uiapi.Event) bool {
		b, err := json.Marshal(e.Data)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Name, b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	write(uiapi.Event{Name: "state", Data: s.ui.State()}) // 一连上先给一份
	hb := time.NewTicker(15 * time.Second)
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-hb.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		case e := <-ch:
			if !write(e) {
				return
			}
		}
	}
}

// ---- 会话 ----

func (s *Server) needAuth() bool { return s.ui.Settings().WebPassword != "" }

func (s *Server) authed(r *http.Request) bool {
	c, err := r.Cookie("gvsid")
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[c.Value]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, c.Value)
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in)
	ip := clientIP(r)
	s.mu.Lock()
	recent := s.fails[ip][:0]
	for _, t := range s.fails[ip] {
		if time.Since(t) < 10*time.Minute {
			recent = append(recent, t)
		}
	}
	s.fails[ip] = recent
	if len(recent) >= 5 {
		s.mu.Unlock()
		writeJSON(w, 429, map[string]any{"error": "失败次数太多,10 分钟后再试"})
		return
	}
	s.mu.Unlock()
	if !s.ui.Settings().CheckWebPassword(in.Password) {
		s.mu.Lock()
		s.fails[ip] = append(s.fails[ip], time.Now())
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"error": "密码不对"})
		return
	}
	tok := make([]byte, 24)
	_, _ = rand.Read(tok)
	id := hex.EncodeToString(tok)
	s.mu.Lock()
	s.sessions[id] = time.Now().Add(30 * 24 * time.Hour)
	delete(s.fails, ip)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "gvsid", Value: id, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600})
	writeJSON(w, 200, map[string]any{"result": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("gvsid"); err == nil {
		s.mu.Lock()
		delete(s.sessions, c.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "gvsid", Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, 200, map[string]any{"result": true})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ---- 路由 ----

func (s *Server) Handler() http.Handler {
	static := http.FileServer(http.FS(web.Dist()))
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/")
		if !s.needAuth() && !isLoopback(r) {
			// 对外监听但还没设密码:外面的人一律挡住,本机(命令行 / 本机浏览器)照常
			writeJSON(w, 403, map[string]any{"error": "NEED_PASSWORD"})
			return
		}
		switch {
		case name == "login" && r.Method == http.MethodPost:
			s.handleLogin(w, r)
			return
		case name == "ping":
			writeJSON(w, 200, map[string]any{"result": map[string]any{"version": buildinfo.Version, "auth": s.needAuth()}})
			return
		}
		if s.needAuth() && !s.authed(r) {
			writeJSON(w, 401, map[string]any{"error": "AUTH_REQUIRED"})
			return
		}
		// 同源检查:浏览器发的写操作必须带同源 Origin(挡住别的网页拿本机 cookie 发请求)
		if r.Method == http.MethodPost {
			if o := r.Header.Get("Origin"); o != "" && !sameOrigin(o, r.Host) {
				writeJSON(w, 403, map[string]any{"error": "跨站请求被拒绝"})
				return
			}
		}
		switch {
		case name == "logout":
			s.handleLogout(w, r)
		case name == "events":
			s.handleEvents(w, r)
		case strings.HasPrefix(name, "diag/"):
			s.handleDiagDownload(w, r, strings.TrimPrefix(name, "diag/"))
		case r.Method == http.MethodPost:
			var args []json.RawMessage
			_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&args)
			res, err := s.ui.Call(name, args)
			if err != nil {
				writeJSON(w, 200, map[string]any{"error": err.Error()})
				return
			}
			if name == "ExportDiag" { // 页面在浏览器里打开这个地址即下载
				if p, ok := res.(string); ok {
					res = "/api/diag/" + filepath.Base(p)
				}
			}
			writeJSON(w, 200, map[string]any{"result": res})
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Cache-Control", "no-store")
		}
		static.ServeHTTP(w, r)
	})
	return mux
}

// isLoopback 请求来自本机。
func isLoopback(r *http.Request) bool {
	ip := net.ParseIP(clientIP(r))
	return ip != nil && ip.IsLoopback()
}

func sameOrigin(origin, host string) bool {
	o := strings.TrimPrefix(strings.TrimPrefix(origin, "http://"), "https://")
	return strings.EqualFold(o, host)
}

func (s *Server) handleDiagDownload(w http.ResponseWriter, r *http.Request, name string) {
	if strings.ContainsAny(name, "/\\") || !strings.HasSuffix(name, ".zip") {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(paths.Diag(), name)
	if _, err := os.Stat(p); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	http.ServeFile(w, r, p)
}
