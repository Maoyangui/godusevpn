package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unsafe"

	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

// PipeName 管道名。ACL:SYSTEM、管理员与安装时登记的控制用户可读写。
const PipeName = `\\.\pipe\godusevpn`

// Address 控制口地址(诊断信息里显示用)。
func Address() string { return PipeName }

// sddlFallback 用于尚未登记控制用户的旧安装。缺少登记文件时只允许
// SYSTEM/Administrators,绝不能退回所有交互用户可写,否则普通本机用户
// 可以借控制管道修改全局禁直连或网卡 IPv6 设置。重新安装后会写入
// 当前安装用户 SID,恢复该用户的非管理员托盘控制能力。
const sddlFallback = "D:P(A;;GA;;;SY)(A;;GA;;;BA)"

var sidPattern = regexp.MustCompile(`^S-\d-\d+(?:-\d+)+$`)

func controllerSIDPath() string { return filepath.Join(paths.DataDir(), "controller.sid") }

// RegisterControllerOwner 在安装时登记当前交互会话的 Windows 用户。UAC
// 可能使用另一位管理员的令牌，不能把安装进程令牌误当成托盘用户。文件只由
// SYSTEM/Administrators 可读,守护进程用它生成命名管道 ACL;普通交互用户
// 即使能发现管道名也不能修改全局禁直连或网卡 IPv6 设置。
func RegisterControllerOwner() error {
	var session uint32
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &session); err != nil {
		return fmt.Errorf("读取安装会话: %w", err)
	}
	if session != 0 {
		// The desktop shell keeps the logged-on user's token across an
		// over-the-shoulder UAC prompt. Also works with Remote Desktop disabled.
		if sid, err := desktopOwnerSID(session); err == nil {
			return RegisterControllerOwnerSID(sid)
		}
		// RDP/kiosk sessions may not have explorer; ask WTS for that exact
		// session rather than selecting an unrelated active-console user.
		name, err := sessionAccountName(session)
		if err != nil {
			return fmt.Errorf("无法确定当前会话的控制用户,请使用 --controller-user 指定: %w", err)
		}
		return RegisterControllerOwnerName(name)
	}
	// Unattended service-session installs have no interactive owner. Keep the
	// installing principal (normally SYSTEM) and the existing admin-only ACL.
	proc, err := windows.GetCurrentProcess()
	if err != nil {
		return fmt.Errorf("获取当前进程令牌: %w", err)
	}
	var tok windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &tok); err != nil {
		return fmt.Errorf("打开安装用户令牌: %w", err)
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return fmt.Errorf("读取安装用户 SID: %w", err)
	}
	return RegisterControllerOwnerSID(tu.User.Sid.String())
}

func desktopOwnerSID(session uint32) (string, error) {
	user32 := windows.NewLazySystemDLL("user32.dll")
	hwnd, _, _ := user32.NewProc("GetShellWindow").Call()
	if hwnd == 0 {
		return "", errors.New("当前会话没有桌面外壳")
	}
	var pid uint32
	user32.NewProc("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	var shellSession uint32
	if pid == 0 || windows.ProcessIdToSessionId(pid, &shellSession) != nil || shellSession != session {
		return "", errors.New("桌面外壳会话不匹配")
	}
	proc, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(proc)
	var token windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &token); err != nil {
		return "", err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return user.User.Sid.String(), nil
}

func sessionAccountName(session uint32) (string, error) {
	wts := windows.NewLazySystemDLL("wtsapi32.dll")
	query := wts.NewProc("WTSQuerySessionInformationW")
	free := wts.NewProc("WTSFreeMemory")
	read := func(class uintptr) (string, error) {
		var buf *uint16
		var size uint32
		ok, _, err := query.Call(0, uintptr(session), class, uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&size)))
		if ok == 0 {
			return "", err
		}
		defer free.Call(uintptr(unsafe.Pointer(buf)))
		if buf == nil || size < 2 {
			return "", nil
		}
		return windows.UTF16ToString(unsafe.Slice(buf, int(size/2))), nil
	}
	name, err := read(5) // WTSUserName
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", errors.New("当前会话没有登录用户")
	}
	domain, err := read(7) // WTSDomainName
	if err != nil {
		return "", err
	}
	if domain != "" {
		name = domain + `\` + name
	}
	return name, nil
}

// RegisterControllerOwnerSID writes an already resolved interactive-user SID.
// The caller is elevated during installation, so resolving the SID before
// writing it avoids accidentally ACLing the pipe to the temporary UAC admin.
func RegisterControllerOwnerSID(sid string) error {
	if !sidPattern.MatchString(sid) {
		return fmt.Errorf("安装用户 SID 无效")
	}
	if err := paths.Ensure(); err != nil {
		return err
	}
	tmp := controllerSIDPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(sid+"\n"), 0o600); err != nil {
		return fmt.Errorf("写入控制用户 SID: %w", err)
	}
	if err := os.Rename(tmp, controllerSIDPath()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("提交控制用户 SID: %w", err)
	}
	return nil
}

// RegisterControllerOwnerName resolves a user name supplied by the installer
// (the original interactive user) while running elevated.
func RegisterControllerOwnerName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("安装用户名称为空")
	}
	account, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	var sidLen, domainLen, use uint32
	_ = windows.LookupAccountName(nil, account, nil, &sidLen, nil, &domainLen, &use)
	if sidLen == 0 {
		return fmt.Errorf("查找安装用户 SID 失败")
	}
	sidBuf := make([]byte, sidLen)
	if domainLen == 0 {
		domainLen = 1
	}
	domain := make([]uint16, domainLen)
	sid := (*windows.SID)(unsafe.Pointer(&sidBuf[0]))
	if err := windows.LookupAccountName(nil, account, sid, &sidLen, &domain[0], &domainLen, &use); err != nil {
		return fmt.Errorf("查找安装用户 SID: %w", err)
	}
	return RegisterControllerOwnerSID(sid.String())
}

// ControllerOwnerRegistered reports whether a valid owner is already stored.
// Upgrades must preserve it: the account supplying a later UAC prompt may be
// a different administrator from the user who owns the running tray.
func ControllerOwnerRegistered() bool {
	b, err := os.ReadFile(controllerSIDPath())
	return err == nil && sidPattern.MatchString(strings.TrimSpace(string(b)))
}

func pipeSDDL() string {
	b, err := os.ReadFile(controllerSIDPath())
	if err != nil {
		return sddlFallback
	}
	sid := strings.TrimSpace(string(b))
	if !sidPattern.MatchString(sid) {
		return sddlFallback
	}
	return "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;" + sid + ")"
}

func listen() (net.Listener, error) {
	return winio.ListenPipe(PipeName, &winio.PipeConfig{SecurityDescriptor: pipeSDDL()})
}

func dial(ctx context.Context) (net.Conn, error) {
	return winio.DialPipeContext(ctx, PipeName)
}

func listenerClosed(err error) bool { return errors.Is(err, winio.ErrPipeListenerClosed) }

// 管道 ACL 只放行当前交互式用户、SYSTEM 与管理员;连不上就是服务没跑或调用者没有交互式会话。
func permissionDenied(error) bool { return false }
