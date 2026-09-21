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
	// CodeRuleSet 规则集读不出来。规则集是在内核启动阶段全部载入的,坏一个就整个起不来,
	// 早先这类失败会落进 E_CORE_START(界面只显示「内核启动失败」),用户完全无从下手。
	CodeRuleSet = "E_RULESET"
	// CodePortBusy 要监听的端口被别人占着(混合端口、Clash API)。同样是启动阶段才暴露。
	CodePortBusy  = "E_PORT_BUSY"
	CodeCoreStart = "E_CORE_START"
	CodeCoreCrash = "E_CORE_CRASH"
	CodeNodeDown  = "E_NODE_DOWN"
	// 隐私保护前置条件未满足时必须退避重试，不能继续启动数据面。
	CodePrivacyGuard = "E_PRIVACY_GUARD"
	CodePrivacyNIC   = "E_PRIVACY_NIC"
	CodeUnknown      = "E_UNKNOWN"
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
	Prepare func(ctx context.Context) ([]byte, error) // 刷新订阅 + 生成配置 + 干跑
	Start   func(cfg []byte) error
	Stop    func() error
	Alive   func() bool                     // 内核还活着吗(每 AliveEvery 查一次)
	Health  func(ctx context.Context) error // 经代理测一次(每 HealthEvery 一次);连续两次失败进 Degraded
	// Recover 线路连着但不通、连续失败到放弃边缘时叫一次,给守护进程一个自救的机会
	// (比如手动选定的节点挂了,换到一个测得通的节点上)。
	// 返回 true = 确实动了什么,再给一轮观察期;false / 未设置 = 直接重建连接。
	Recover  func(ctx context.Context) bool
	OnChange func(Snapshot)
	Logf     func(format string, a ...any)

	AliveEvery  time.Duration
	HealthEvery time.Duration
	// DegradedEvery 进了 Degraded 之后健康检查改成多久一次(默认 degradedEvery)。
	DegradedEvery time.Duration
	Backoff       []time.Duration // 重试间隔序列,超过最后一项按最后一项
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
	// kick 让 watch 立刻做一次健康检查,不等定时器。会话看护判定当前节点的会话已废时会敲一下:
	// 三分钟一次的常规检查对"整机断网"来说太慢,而看护那边已经有证据了。容量 1,敲多少下都只算一次。
	kick chan struct{}
}

func New(d Deps) *Machine {
	if d.AliveEvery == 0 {
		d.AliveEvery = 10 * time.Second
	}
	if d.HealthEvery == 0 {
		d.HealthEvery = 3 * time.Minute
	}
	if d.DegradedEvery == 0 {
		d.DegradedEvery = degradedEvery
	}
	if len(d.Backoff) == 0 {
		d.Backoff = []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second, 60 * time.Second}
	}
	if d.Logf == nil {
		d.Logf = func(string, ...any) {}
	}
	return &Machine{d: d, snap: Snapshot{Status: Disconnected, Since: time.Now().Unix()}, kick: make(chan struct{}, 1)}
}

// CheckNow 请求立刻做一次健康检查(连接期间才有意义;没在连就丢掉)。不阻塞。
func (m *Machine) CheckNow() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
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
	go m.run(ctx, false)
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

// RestartChecked is used for privacy-setting transitions. Unlike Restart, it
// returns only after Start has succeeded. rollback runs before retries whenever
// validation or startup fails, so the retry loop cannot observe rejected settings.
func (m *Machine) RestartChecked(rollback func() error) error {
	if !m.Wanted() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cfg, err := m.d.Prepare(ctx)
	if err != nil {
		if rollback != nil {
			err = errors.Join(err, rollback())
		}
		return err
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if !m.Wanted() {
		return nil
	}
	m.stopLoop()
	m.set(Starting, nil)
	if err = m.d.Start(cfg); err != nil {
		_ = m.d.Stop()
		if rollback != nil {
			err = errors.Join(err, rollback())
		}
		m.set(Failed, err)
		m.connect() // retry only after the previous settings are restored
		return err
	}
	m.mu.Lock()
	runCtx, runCancel := context.WithCancel(context.Background())
	m.cancel = runCancel
	m.snap.Retries = 0
	m.wg.Add(1)
	m.mu.Unlock()
	go m.run(runCtx, true)
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

func (m *Machine) run(ctx context.Context, started bool) {
	defer m.wg.Done()
	attempt := 0
	for ctx.Err() == nil {
		if !started {
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
		}
		started = false
		attempt = 0
		m.mu.Lock()
		m.snap.Retries = 0
		m.mu.Unlock()
		m.set(Connected, nil)
		rebuild, cause := m.watch(ctx)
		if !rebuild {
			return // 用户断开
		}
		// 内核死了:停干净,退避后从头来
		_ = m.d.Stop()
		attempt++
		if cause == nil {
			cause = Errf(CodeCoreCrash, "内核异常退出,正在重连")
		}
		m.set(Failed, cause)
		if !sleepCtx(ctx, m.backoff(attempt)) {
			return
		}
	}
}

// degradedEvery 进了 Degraded 之后健康检查改成多久一次。
// 正常间隔是三分钟,那是"一切正常时别瞎折腾";已经发现不对劲了就得盯紧点,
// 否则光是确认"真的救不回来"就要十分钟,用户对着一块写着「已连接 · 节点不稳」、实际一点网都没有的电视干等。
const degradedEvery = 30 * time.Second

// degradedGiveUp 连续失败到第几次就不再等了。第 2 次进 Degraded,之后按 degradedEvery 复查,
// 到第 4 次(约 1 分钟)还不行就重建连接。
const degradedGiveUp = 4

// watch 连接期间盯着内核:活着就定期做健康检查;返回 false = 被取消,true = 该重建连接了。
//
// 注意"返回 true"以前只表示"内核死了",现在也包括"内核还在跑,但这条线路已经救不回来"。
// 调用方(run)两种情况的处理本来就一样:停干净、退避、从头 Prepare + Start。
// 早先没有这条出路 —— 手动选定节点的用户夜里节点被墙,界面就永久停在「已连接 · 节点不稳」,
// 既不会自己换线也不会重连,开着「全局禁直连」时连直连都没有,只能人去点断开再连。
func (m *Machine) watch(ctx context.Context) (bool, error) {
	alive := time.NewTicker(m.d.AliveEvery)
	health := time.NewTicker(m.d.HealthEvery)
	defer alive.Stop()
	defer health.Stop()
	fails := 0
	tried := false // 这一轮不健康期间已经叫过一次自救了
	// 断开期间攒下的 kick 作废:那是对上一条连接的判定,新连接刚起来不该平白多查一次
	select {
	case <-m.kick:
	default:
	}
	// 一次健康检查。返回 true = 该重建连接了(内核死了、或这条线路救不回来)。
	check := func() (bool, error) {
		if m.d.Health == nil {
			return false, nil
		}
		err := m.d.Health(ctx)
		cur := m.Snapshot().Status
		if err == nil {
			if fails > 0 {
				health.Reset(m.d.HealthEvery) // 好了,回到正常节奏
			}
			fails, tried = 0, false
			if cur == Degraded {
				m.set(Connected, nil)
			}
			return false, nil
		}
		if CodeOf(err) == CodeCoreCrash {
			return true, err
		}
		// Privacy protection is a hard runtime invariant. Do not leave a
		// working data plane up while the guard or managed IPv6 state is
		// missing; stop and rebuild only after the next attempt verifies it.
		if code := CodeOf(err); code == CodePrivacyGuard || code == CodePrivacyNIC {
			return true, err
		}
		fails++
		// 第一次不通就把节奏收紧:确认"真的不对劲"只要再等一个短周期,而不是再等三分钟。
		// 以前是第二次失败才收紧,于是从出事到进 Degraded 至少六分钟,到自救要七分钟。
		health.Reset(m.d.DegradedEvery)
		if fails >= 2 && cur != Degraded {
			m.set(Degraded, err)
		}
		if fails < degradedGiveUp {
			return false, nil
		}
		// 先给守护进程一次自救的机会(换一个测得通的节点),换过就重新给一轮观察期
		if m.d.Recover != nil && !tried {
			tried = true
			if m.d.Recover(ctx) {
				fails = 1
				return false, nil
			}
		}
		m.d.Logf("连续 %d 次健康检查都不通,重建连接: %v", fails, err)
		return true, err
	}
	for {
		select {
		case <-ctx.Done():
			return false, nil
		case <-alive.C:
			if m.d.Alive != nil && !m.d.Alive() {
				return true, Errf(CodeCoreCrash, "内核异常退出,正在重连")
			}
		case <-health.C:
			if rebuild, cause := check(); rebuild {
				return true, cause
			}
		case <-m.kick:
			if rebuild, cause := check(); rebuild {
				return true, cause
			}
		}
	}
}
