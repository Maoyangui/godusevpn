package ipc

import (
	"os/exec"
	"regexp"
	"strconv"
	"testing"
)

// 控制管道的对端只认:本进程、服务管理器登记的佛跳墙服务进程、LocalSystem / 提权管理员进程。
// 服务停着的空档里别的账户抢注同名管道,界面不能连上去把订阅地址和设置交给它。
func TestTrustedPipeServer(t *testing.T) {
	never := func(uint32) bool { return false }
	always := func(uint32) bool { return true }
	for _, c := range []struct {
		name               string
		pid, self, service uint32
		privileged         func(uint32) bool
		want               bool
	}{
		{"本进程(测试、进程内)", 100, 100, 0, never, true},
		{"服务管理器登记的服务进程", 200, 100, 200, never, true},
		{"提权 / SYSTEM 进程(管理员终端里前台 run)", 300, 100, 0, always, true},
		{"别的账户抢注的管道", 400, 100, 200, never, false},
		{"服务没在跑、对端是普通进程", 400, 100, 0, never, false},
		{"查不到对端进程号", 0, 100, 0, always, false},
	} {
		if got := trustedPipeServer(c.pid, c.self, c.service, c.privileged); got != c.want {
			t.Errorf("%s:得到 %v,应为 %v", c.name, got, c.want)
		}
	}
}

// servicePID 查到的就是服务管理器里那个进程(只读;本机没装服务就跳过)。
func TestServicePIDMatchesSCM(t *testing.T) {
	out, err := exec.Command("sc.exe", "queryex", serviceName).CombinedOutput()
	m := regexp.MustCompile(`PID\s*:\s*(\d+)`).FindSubmatch(out)
	if err != nil || m == nil {
		t.Skipf("本机没装佛跳墙服务: %v", err)
	}
	want, _ := strconv.Atoi(string(m[1]))
	if got := servicePID(); int(got) != want {
		t.Fatalf("servicePID = %d,sc queryex 说是 %d", got, want)
	}
}
