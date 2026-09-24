// Package svc Windows 服务壳:注册 / 卸载 / 启停,以及作为服务运行时的控制循环。
// 服务以 SYSTEM 身份自动启动,失败后由 SCM 按恢复策略拉起。
package svc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	Name        = "godusevpn"
	DisplayName = "佛跳墙"
	Description = "佛跳墙 后台服务:运行内核、管理 TUN 网卡、路由与 DNS。"
)

type handler struct {
	run func(ctx context.Context) error
}

func (h *handler) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	s <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.run(ctx) }()
	s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending}
				cancel()
				select {
				case <-done:
				case <-time.After(20 * time.Second):
				}
				return false, 0
			}
		case err := <-done:
			cancel()
			if err != nil {
				return true, 1
			}
			return false, 0
		}
	}
}

// IsService 当前进程是不是被 SCM 拉起的。
func IsService() bool {
	ok, _ := svc.IsWindowsService()
	return ok
}

// Run 作为服务运行(阻塞到服务停止)。
func Run(run func(ctx context.Context) error) error {
	return svc.Run(Name, &handler{run: run})
}

// Install 注册服务:自动启动,失败 5 秒后重启。
func Install(exe string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器(需要管理员权限): %w", err)
	}
	defer m.Disconnect()
	if s, err := m.OpenService(Name); err == nil {
		// 升级会先 stop 但必须保留服务对象:删除服务会触发卸载语义,
		// 可能撤销持久闸并恢复网卡 IPv6。更新现有二进制路径后复用同一
		// 服务对象,再由调用方 Start,整个替换期间保护保持不变。
		defer s.Close()
		cfg, err := s.Config()
		if err != nil {
			return fmt.Errorf("读取已有服务配置: %w", err)
		}
		cfg.BinaryPathName = syscall.EscapeArg(exe) + " service"
		// 启动类型与依赖也一并恢复:被优化工具 / 用户设成「禁用」或「手动」的服务,以前靠「修复」的 uninstall + install
		// 重建;现在「修复」不再卸载(那会撤闸),这里把它们改回来。开机后闸要靠服务起来重装,自动启动是前提。
		cfg.StartType = mgr.StartAutomatic
		cfg.Dependencies = []string{"BFE", "Tcpip"}
		if err := s.UpdateConfig(cfg); err != nil {
			return fmt.Errorf("更新已有服务配置: %w", err)
		}
		_ = exec.Command("sc.exe", "sdset", Name, userStartStopSDDL).Run()
		_ = s.SetRecoveryActions(recoveryActions, 86400)
		return nil
	}
	s, err := m.CreateService(Name, exe, mgr.Config{
		DisplayName: DisplayName, Description: Description, StartType: mgr.StartAutomatic,
		Dependencies: []string{"BFE", "Tcpip"}, // 闸靠 BFE(基础筛选引擎),隧道靠 TCP/IP:等它们起来再起
	}, "service")
	if err != nil {
		return fmt.Errorf("创建服务: %w", err)
	}
	defer s.Close()
	// 让本机已登录用户能启停服务(托盘"退出"要把服务一起停掉,登录时再拉起,都不弹 UAC)
	_ = exec.Command("sc.exe", "sdset", Name, userStartStopSDDL).Run()
	_ = s.SetRecoveryActions(recoveryActions, 86400)
	return nil
}

// recoveryActions 服务崩了由 SCM 拉起:5 秒、15 秒、60 秒。
var recoveryActions = []mgr.RecoveryAction{
	{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
	{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
	{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
}

// Uninstall 停止并删除服务。
func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器(需要管理员权限): %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		// 本来就不在(用户先手动跑过 uninstall、安装时服务没注册上、被 sc delete 过)= 已经卸掉了,幂等。
		// 以前报错,卸载程序看到非零退出码就当"闸撤不掉"中止,而且每次重试都一样,永远卸不掉。
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil
		}
		return fmt.Errorf("打开服务: %w", err)
	}
	defer s.Close()
	// 删之前必须停稳。DeleteService 对还在跑的服务只做"标记删除"、照样返回成功,进程接着跑:调用方(svc uninstall)
	// 撤闸到这里之间要是有人打开客户端把服务拉了起来,而以前这里只发一次 Stop、结果不看(启动中的服务会拒收),
	// 活着的守护进程就会按"想连"把调用方随后的第二次撤闸装回去,接着程序被删,留下一道谁都撤不掉的闸。
	// 停不下来就不删,返回错误让卸载中止。
	if err := stopHard(s, 60*time.Second); err != nil {
		return fmt.Errorf("服务停不下来,没有删除: %w", err)
	}
	// 已经被标记删除(别处 sc delete 过、服务管理器还开着句柄):句柄一关它就没了,算卸掉了
	if err := s.Delete(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_MARKED_FOR_DELETE) {
		return err
	}
	// 删完再确认一次:上面停稳到 Delete 之间要是又有人拉起了它,它还活着。标记删除以后 StartService 一律失败
	// (ERROR_SERVICE_MARKED_FOR_DELETE),所以这里停稳就是永久停稳,调用方的第二次撤闸不会再被装回去。
	if err := stopHard(s, 60*time.Second); err != nil {
		return fmt.Errorf("服务已标记删除,但还在运行、停不下来: %w", err)
	}
	return nil
}

// stopper 是 stopHard 用到的 *mgr.Service 那两个方法;抽出来好在测试里用假的服务驱动。
type stopper interface {
	Query() (svc.Status, error)
	Control(c svc.Cmd) (svc.Status, error)
}

// stopPoll 轮询间隔;stopResend 发出的 Stop 被接受后,过了这么久还在"运行 / 启动中"就当新实例重发。测试里调小。
var stopPoll, stopResend = 300 * time.Millisecond, 2 * time.Second

// stopHard 用这个句柄把服务停下来,确认真停稳了(Stopped)才返回 nil。
// 启动中(START_PENDING)的服务不接受 Stop,ControlService 返回 ERROR_SERVICE_CANNOT_ACCEPT_CTRL;以前只发一次就干等,
// 等来的是一个跑起来的服务。这里在它"运行 / 启动中"时重发。被接受的 Stop 几毫秒内就会让守护进程报"停止中"
// (Execute 收到 Stop 第一件事就是报 StopPending),所以发出后过了 2 秒还在"运行 / 启动中",就是又被人拉起了
// 一个新实例,再发。不对同一个实例连发:x/sys 的服务循环在处理 Stop 期间不再收新的控制命令,多发的那条会卡住。
func stopHard(s stopper, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var sent time.Time
	var lastErr error
	for {
		st, err := s.Query()
		switch {
		case err != nil:
			lastErr = err
		case st.State == svc.Stopped:
			return nil
		case (st.State == svc.Running || st.State == svc.StartPending) && time.Since(sent) > stopResend:
			if _, err := s.Control(svc.Stop); err == nil {
				sent = time.Now()
			} else if !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) && !errors.Is(err, windows.ERROR_SERVICE_CANNOT_ACCEPT_CTRL) {
				lastErr = err
			}
		}
		if !time.Now().Before(deadline) {
			if lastErr != nil {
				return fmt.Errorf("服务停止超时: %w", lastErr)
			}
			return errors.New("服务停止超时")
		}
		time.Sleep(stopPoll)
	}
}

func Start() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器(需要管理员权限): %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return errors.New("服务未安装")
	}
	defer s.Close()
	if err := s.Start(); err != nil {
		return err
	}
	if !waitState(s, svc.Running, 20*time.Second) {
		return errors.New("服务启动超时")
	}
	return nil
}

func Stop() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器(需要管理员权限): %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil // 幂等:服务本来就没有可停
		}
		return errors.New("服务未安装")
	}
	defer s.Close()
	// 启动中的服务会拒收 Stop:以前直接把 1061 当失败返回,调用方(stopAndWait)只剩干等,等来一个跑起来的服务
	return stopHard(s, 20*time.Second)
}

// userStartStopSDDL 默认服务 ACL 基础上给 Authenticated Users 加 RP(启动)与 WP(停止)。
const userStartStopSDDL = "D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWRPWPLOCRRC;;;AU)(A;;CCLCSWLOCRRC;;;IU)(A;;CCLCSWLOCRRC;;;SU)S:(AU;FA;CCDCLCSWRPWPDTLOCRSDRCWDWO;;WD)"

// openUser 以普通用户能拿到的权限打开服务。
func openUser(access uint32) (windows.Handle, windows.Handle, error) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, 0, err
	}
	s, err := windows.OpenService(m, windows.StringToUTF16Ptr(Name), access)
	if err != nil {
		windows.CloseServiceHandle(m)
		return 0, 0, errors.New("服务未安装")
	}
	return m, s, nil
}

// StartUser 普通用户启动服务(安装时已放开 ACL)。已在运行返回 nil。
func StartUser() error {
	m, s, err := openUser(windows.SERVICE_START | windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(m)
	defer windows.CloseServiceHandle(s)
	var st windows.SERVICE_STATUS
	if windows.QueryServiceStatus(s, &st) == nil && (st.CurrentState == windows.SERVICE_RUNNING || st.CurrentState == windows.SERVICE_START_PENDING) {
		return nil
	}
	if err := windows.StartService(s, 0, nil); err != nil {
		return fmt.Errorf("启动服务: %w", err)
	}
	return nil
}

// StopUser 普通用户停止服务;未运行返回 nil。
func StopUser() error {
	m, s, err := openUser(windows.SERVICE_STOP | windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(m)
	defer windows.CloseServiceHandle(s)
	var st windows.SERVICE_STATUS
	if windows.QueryServiceStatus(s, &st) == nil && st.CurrentState == windows.SERVICE_STOPPED {
		return nil
	}
	if err := windows.ControlService(s, windows.SERVICE_CONTROL_STOP, &st); err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return nil
		}
		return fmt.Errorf("停止服务: %w", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if windows.QueryServiceStatus(s, &st) == nil && st.CurrentState == windows.SERVICE_STOPPED {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("服务停止超时")
}

// QueryStatus 不需要管理员权限的状态查询(托盘客户端用):只申请"连接"和"查询状态"两个权限。
func QueryStatus() string {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "unknown"
	}
	defer windows.CloseServiceHandle(m)
	s, err := windows.OpenService(m, windows.StringToUTF16Ptr(Name), windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return "not-installed"
	}
	defer windows.CloseServiceHandle(s)
	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(s, &st); err != nil {
		return "unknown"
	}
	switch st.CurrentState {
	case windows.SERVICE_RUNNING:
		return "running"
	case windows.SERVICE_STOPPED:
		return "stopped"
	case windows.SERVICE_START_PENDING:
		return "starting"
	case windows.SERVICE_STOP_PENDING:
		return "stopping"
	default:
		return "unknown"
	}
}

// Status 服务状态文本:not-installed / stopped / running / …
func Status() string {
	m, err := mgr.Connect()
	if err != nil {
		return "unknown"
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return "not-installed"
	}
	defer s.Close()
	st, err := s.Query()
	if err != nil {
		return "unknown"
	}
	switch st.State {
	case svc.Running:
		return "running"
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	default:
		return "unknown"
	}
}

func waitState(s *mgr.Service, want svc.State, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, err := s.Query()
		if err == nil && st.State == want {
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

// Kind 初始化系统名字(与 Linux 版接口一致)。
func Kind() string { return "windows-service" }
