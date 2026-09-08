// Package svc Windows 服务壳:注册 / 卸载 / 启停,以及作为服务运行时的控制循环。
// 服务以 SYSTEM 身份自动启动,失败后由 SCM 按恢复策略拉起。
package svc

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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
		s.Close()
		return errors.New("服务已存在,先卸载再安装")
	}
	s, err := m.CreateService(Name, exe, mgr.Config{
		DisplayName: DisplayName, Description: Description, StartType: mgr.StartAutomatic,
	}, "service")
	if err != nil {
		return fmt.Errorf("创建服务: %w", err)
	}
	defer s.Close()
	// 让本机已登录用户能启停服务(托盘"退出"要把服务一起停掉,登录时再拉起,都不弹 UAC)
	_ = exec.Command("sc.exe", "sdset", Name, userStartStopSDDL).Run()
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 86400)
	return nil
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
		return errors.New("服务不存在")
	}
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State != svc.Stopped {
		_, _ = s.Control(svc.Stop)
		waitState(s, svc.Stopped, 20*time.Second)
	}
	return s.Delete()
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
		return errors.New("服务未安装")
	}
	defer s.Close()
	if _, err := s.Control(svc.Stop); err != nil {
		return err
	}
	if !waitState(s, svc.Stopped, 20*time.Second) {
		return errors.New("服务停止超时")
	}
	return nil
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
