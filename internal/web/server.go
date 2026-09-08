// Package web Linux 守护进程内置的面板:把 web/dist 里的竖版页面从 HTTP 端出去,
// 页面里的 window.go.main.App.* 由 dist/api.js 映射成 POST /api/<方法>,事件(state / traffic / update-progress)走 SSE。
// 面板绑定非回环地址时要密码(设置里的 webPassword),cookie 会话;命令行走 Unix socket 不需要密码。
package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/clash"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/settings"
	"github.com/Maoyangui/godusevpn/internal/update"
	"github.com/Maoyangui/godusevpn/web"
)

// Backend 守护进程给面板的接口:进程内直接调控制口方法。
type Backend interface {
	Dispatch(method string, params json.RawMessage) (any, error)
	Methods() map[string]bool
	Logf(format string, a ...any)
}

type event struct {
	name string
	data any
}

type prefs struct {
	Lang  string `json:"lang"`
	Theme string `json:"theme"`
}

type Server struct {
	b   Backend
	mu  sync.Mutex
	ctx context.Context

	sessions map[string]time.Time   // 会话 → 到期
	fails    map[string][]time.Time // 登录失败时间(按来源 IP)
	subs     map[chan event]struct{}

	up, down    int64
	trafficStop context.CancelFunc
	clashKey    string

	prefs     prefs
	update    *update.Release
	lastCheck time.Time
	updating  bool
	lastState string
}

func New(b Backend) *Server {
	s := &Server{b: b, sessions: map[string]time.Time{}, fails: map[string][]time.Time{}, subs: map[chan event]struct{}{}}
	if raw, err := os.ReadFile(paths.UIPrefs()); err == nil {
		_ = json.Unmarshal(raw, &s.prefs)
	}
	if s.prefs.Lang == "" {
		s.prefs.Lang = "zh" // 页面把"语言变了"当作要整页重绘,空值不能给出去
	}
	if s.prefs.Theme == "" {
		s.prefs.Theme = "system"
	}
	return s
}

// Serve 在 listen 上跑到 ctx 结束。同时跑状态推送与速度流。
func (s *Server) Serve(ctx context.Context, listen string) error {
	s.ctx = ctx
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("面板监听 %s: %w", listen, err)
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go s.loop(ctx)
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	s.b.Logf("面板监听 http://%s/", listen)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// loop 每 1.5 秒推一次状态(有人订阅时),并按内核状态开关速度流;连上网后顺手查一次更新。
func (s *Server) loop(ctx context.Context) {
	t := time.NewTicker(1500 * time.Millisecond)
	defer t.Stop()
	go s.updateLoop(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st := s.state()
			s.manageTraffic(ctx, st)
			s.mu.Lock()
			justConnected := st.View.State.Status == "connected" && s.lastState != "connected"
			s.lastState = string(st.View.State.Status)
			n := len(s.subs)
			s.mu.Unlock()
			if n > 0 {
				s.broadcast("state", st)
			}
			if justConnected {
				go s.pokeUpdate(false)
			}
		}
	}
}

// ---- 事件 ----

func (s *Server) broadcast(name string, data any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- event{name, data}:
		default: // 慢客户端丢一条
		}
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", 500)
		return
	}
	ch := make(chan event, 32)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	write := func(e event) bool {
		b, err := json.Marshal(e.data)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.name, b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	write(event{"state", s.state()}) // 一连上先给一份
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

// ---- 速度流(同 Windows 客户端的做法) ----

func (s *Server) manageTraffic(ctx context.Context, st uiState) {
	running := st.View.State.Status == "connected" || st.View.State.Status == "degraded"
	if !running {
		s.mu.Lock()
		stop := s.trafficStop
		s.trafficStop, s.clashKey, s.up, s.down = nil, "", 0, 0
		s.mu.Unlock()
		if stop != nil {
			stop()
		}
		return
	}
	info, err := s.clashInfo()
	if err != nil || !info.Running {
		return
	}
	key := fmt.Sprintf("%d:%s", info.Port, info.Secret)
	s.mu.Lock()
	if s.clashKey == key && s.trafficStop != nil {
		s.mu.Unlock()
		return
	}
	if s.trafficStop != nil {
		s.trafficStop()
	}
	sctx, stop := context.WithCancel(ctx)
	s.trafficStop, s.clashKey = stop, key
	s.mu.Unlock()
	go func() {
		c := clash.New(info.Port, info.Secret)
		for sctx.Err() == nil {
			_ = c.Traffic(sctx, func(up, down int64) {
				s.mu.Lock()
				s.up, s.down = up, down
				n := len(s.subs)
				s.mu.Unlock()
				if n > 0 {
					s.broadcast("traffic", map[string]int64{"up": up, "down": down})
				}
			})
			if sctx.Err() == nil {
				time.Sleep(2 * time.Second)
			}
		}
	}()
}

func (s *Server) clashInfo() (ipc.ClashInfo, error) {
	var info ipc.ClashInfo
	err := s.dispatch(ipc.MGetClashInfo, nil, &info)
	return info, err
}

func (s *Server) clashClient() (*clash.Client, error) {
	info, err := s.clashInfo()
	if err != nil {
		return nil, err
	}
	if !info.Running {
		return nil, errors.New("内核未运行")
	}
	return clash.New(info.Port, info.Secret), nil
}

// dispatch 调守护进程方法并把结果解到 out。
func (s *Server) dispatch(method string, params any, out any) error {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		raw = b
	}
	res, err := s.b.Dispatch(method, raw)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	b, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (s *Server) settings() settings.Settings {
	var st settings.Settings
	_ = s.dispatch(ipc.MGetSettings, nil, &st)
	return st
}

// ---- 会话 ----

func (s *Server) needAuth() bool { return s.settings().WebPassword != "" }

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
	if !s.settings().CheckWebPassword(in.Password) {
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
			res, err := s.api(name, args)
			if err != nil {
				writeJSON(w, 200, map[string]any{"error": err.Error()})
				return
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

func sameOrigin(origin, host string) bool {
	o := strings.TrimPrefix(strings.TrimPrefix(origin, "http://"), "https://")
	return strings.EqualFold(o, host)
}

func (s *Server) handleDiagDownload(w http.ResponseWriter, r *http.Request, name string) {
	if strings.ContainsAny(name, "/\\") || !strings.HasSuffix(name, ".zip") {
		http.NotFound(w, r)
		return
	}
	p := paths.Diag() + string(os.PathSeparator) + name
	if _, err := os.Stat(p); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	http.ServeFile(w, r, p)
}

// 静态资源里没有的路径交给 index(单页);目前页面不用路由,留着。
var _ fs.FS = web.Dist()
