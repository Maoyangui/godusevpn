// Package state 连接状态机:唯一的事实源。所有入口(连接 / 断开 / 唤醒 / 内核崩溃 / 配置变化)都汇到同一条路径:
//
//	Disconnected → Preparing → Starting → Connected ⇄ Degraded → Stopping → Disconnected
//	                   └──────── Error(带错误码,退避后自动重试)◀────────┘
//
// 想连(wanted)和当前状态分开记:用户点了连接,哪怕内核崩了、订阅拉不到,也会一直重试到成功或用户点断开。
package state

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Status string

const (
	Disconnected Status = "disconnected"
	Preparing    Status = "preparing"
	Starting     Status = "starting"
	Connected    Status = "connected"
	Degraded     Status = "degraded"
	Stopping     Status = "stopping"
	Failed       Status = "error"
)

// 错误码:界面按码显示文案与建议动作。
const (
	CodeProfileMissing = "E_PROFILE_MISSING"
	CodeProfileNet     = "E_PROFILE_NET"
	CodeProfileURL     = "E_PROFILE_URL" // 订阅链接本身不完整(比如被脱敏成 /sub/***),要用户重新填
	CodeProfileAuth    = "E_PROFILE_AUTH"
	CodeProfileParse   = "E_PROFILE_PARSE"
	CodeConfig         = "E_CONFIG"
	CodeTunDriver      = "E_TUN_DRIVER"
	CodeRouteConflict  = "E_ROUTE_CONFLICT"
	CodeCoreStart      = "E_CORE_START"
	CodeCoreCrash      = "E_CORE_CRASH"
	CodeNodeDown       = "E_NODE_DOWN"
	CodeUnknown        = "E_UNKNOWN"
)

// Error 带码的错误。
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

func Errf(code, format string, a ...any) error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// CodeOf 取错误码;没有码的按未知。
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeUnknown
}

type Snapshot struct {
	Status  Status `json:"status"`
	Since   int64  `json:"since"`
	Code    string `json:"code,omitempty"`
	Error   string `json:"error,omitempty"`
	Retries int    `json:"retries"`
	Wanted  bool   `json:"wanted"`
}

// Deps 状态机需要的动作,由 daemon 注入。
type Deps struct {
	Prepare  func(ctx context.Context) ([]byte, error) // 刷新订阅 + 生成配置 + 干跑
	Start    func(cfg []byte) error
	Stop     func() error
	Alive    func() bool                     // 内核还活着吗(每 AliveEvery 查一次)
	Health   func(ctx context.Context) error // 经代理测一次(每 HealthEvery 一次);连续两次失败进 Degraded
	OnChange func(Snapshot)
	Logf     func(format string, a ...any)

	AliveEvery  time.Duration
	HealthEvery time.Duration
	Backoff     []time.Duration // 重试间隔序列,超过最后一项按最后一项
}

type Machine struct {
	// opMu 生命周期串行锁:Connect / Disconnect / Restart 的"停旧起新"那一段不许交错。
	// 只保护动作顺序,不保护字段(字段仍归 mu 管);取锁顺序固定是 opMu → mu,不会成环。
	// Restart 里备配置那一步(最长三分钟)**不持这把锁**,不然用户点断开会被堵到 IPC 超时。
	opMu   sync.Mutex
	mu     sync.Mutex
	d      Deps
	snap   Snapshot
	wanted bool
	cancel context.CancelFunc
	wg     sync.WaitGroup
	pre    []byte // Restart 提前备好的配置:下一轮 run 直接用它,不再 Prepare 一遍
}

func New(d Deps) *Machine {
	if d.AliveEvery == 0 {
		d.AliveEvery = 10 * time.Second
	}
	if d.HealthEvery == 0 {
		d.HealthEvery = 3 * time.Minute
	}
	if len(d.Backoff) == 0 {
		d.Backoff = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second, 60 * time.Second}
	}
	if d.Logf == nil {
		d.Logf = func(string, ...any) {}
	}
	return &Machine{d: d, snap: Snapshot{Status: Disconnected, Since: time.Now().Unix()}}
}

func (m *Machine) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snap
}

func (m *Machine) Wanted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.wanted
}

func (m *Machine) set(st Status, err error) {
	m.mu.Lock()
	m.snap.Status, m.snap.Since, m.snap.Wanted = st, time.Now().Unix(), m.wanted
	if err != nil {
		m.snap.Code, m.snap.Error = CodeOf(err), err.Error()
	} else {
		m.snap.Code, m.snap.Error = "", ""
	}
	s := m.snap
	m.mu.Unlock()
	if m.d.OnChange != nil {
		m.d.OnChange(s)
	}
}

// Connect 记下"想连"并启动循环;已在跑则无操作。
func (m *Machine) Connect() {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.connect()
}

// connect 调用方必须已经持有 opMu。
func (m *Machine) connect() {
	m.mu.Lock()
	m.wanted = true
	if m.cancel != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.snap.Retries = 0
	m.wg.Add(1)
	m.mu.Unlock()
	go m.run(ctx)
}

// Disconnect 用户主动断开:记下"不想连",停循环,停内核。
func (m *Machine) Disconnect() {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	m.wanted = false
	m.mu.Unlock()
	m.stopLoop()
	m.set(Disconnected, nil)
}

// Restart 配置或订阅变了:想连的话换新配置重来一遍,不想连的什么都不做。
// 先把新配置备好(拉订阅、解析节点地址、生成、干跑)再停旧内核 —— 准备阶段旧内核还在跑,
// 断流的窗口只剩停与起那一下;新配置备不出来就不动旧的,原样连着并返回错误。
func (m *Machine) Restart() error {
	if !m.Wanted() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cfg, err := m.d.Prepare(ctx) // 这一步最长三分钟,不能持着 opMu —— 那会把用户点的断开堵到 IPC 超时
	if err != nil {
		m.d.Logf("新配置准备失败,保持当前连接: %v", err)
		return err
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	// 备配置这几分钟里用户可能已经点了断开。不复查的话下面的 connect() 会把 wanted 改回 true、
	// 隧道自己回来,而磁盘上记的是"不想连",闸也在断开时撤掉了没人再装 —— 成了"连着但没闸"。
	// 这份 cfg 直接丢掉是对的:下次用户点连接会重新备一份,不用担心这份已经放旧了。
	if !m.Wanted() {
		m.d.Logf("新配置备好时用户已经断开,不再重连")
		return nil
	}
	m.mu.Lock()
	m.pre = cfg
	m.mu.Unlock()
	m.stopLoop()
	m.connect()
	return nil
}

// stopLoop 停掉当前这一轮 run 并停内核。**调用方必须持有 opMu** —— 不然两个人同时进来,
// 后到的那个会发现 cancel 已被取走而直接返回,于是旧的 stop() 和新的 start() 交错着跑。
func (m *Machine) stopLoop() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	m.wg.Wait()
	m.set(Stopping, nil)
	if err := m.d.Stop(); err != nil {
		m.d.Logf("停止内核: %v", err)
	}
}

func (m *Machine) backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(m.d.Backoff) {
		attempt = len(m.d.Backoff)
	}
	return m.d.Backoff[attempt-1]
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (m *Machine) run(ctx context.Context) {
	defer m.wg.Done()
	attempt := 0
	for ctx.Err() == nil {
		m.set(Preparing, nil)
		m.mu.Lock()
		cfg := m.pre // Restart 备好的,只用一次
		m.pre = nil
		m.mu.Unlock()
		var err error
		if cfg == nil {
			cfg, err = m.d.Prepare(ctx)
		}
		if err == nil {
			m.set(Starting, nil)
			err = m.d.Start(cfg)
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			_ = m.d.Stop()
			attempt++
			m.mu.Lock()
			m.snap.Retries = attempt
			m.mu.Unlock()
			m.set(Failed, err)
			m.d.Logf("连接失败(第 %d 次,%s 后重试): %v", attempt, m.backoff(attempt), err)
			if !sleepCtx(ctx, m.backoff(attempt)) {
				return
			}
			continue
		}
		attempt = 0
		m.mu.Lock()
		m.snap.Retries = 0
		m.mu.Unlock()
		m.set(Connected, nil)
		if !m.watch(ctx) {
			return // 用户断开
		}
		// 内核死了:停干净,退避后从头来
		_ = m.d.Stop()
		attempt++
		m.set(Failed, Errf(CodeCoreCrash, "内核异常退出,正在重连"))
		if !sleepCtx(ctx, m.backoff(attempt)) {
			return
		}
	}
}

// watch 连接期间盯着内核:活着就定期做健康检查;返回 false = 被取消,true = 内核死了。
func (m *Machine) watch(ctx context.Context) bool {
	alive := time.NewTicker(m.d.AliveEvery)
	health := time.NewTicker(m.d.HealthEvery)
	defer alive.Stop()
	defer health.Stop()
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return false
		case <-alive.C:
			if m.d.Alive != nil && !m.d.Alive() {
				return true
			}
		case <-health.C:
			if m.d.Health == nil {
				continue
			}
			err := m.d.Health(ctx)
			cur := m.Snapshot().Status
			if err == nil {
				fails = 0
				if cur == Degraded {
					m.set(Connected, nil)
				}
				continue
			}
			if CodeOf(err) == CodeCoreCrash {
				return true
			}
			fails++
			if fails >= 2 && cur != Degraded {
				m.set(Degraded, err)
			}
		}
	}
}
