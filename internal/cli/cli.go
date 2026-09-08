// Package cli 排障与脚本用的命令行,Windows 的 godusevpn-cli 与 Linux 的 godusevpn 共用,都走本机控制口。
//
//	status                 当前状态、模式、节点、订阅
//	connect / disconnect
//	mode rule|global|direct
//	nodes                  节点列表(当前项带 *)
//	select <节点名|auto>
//	test [节点名]          经该节点(默认当前 proxy 组)测延迟
//	profile [订阅地址]     查看或设置订阅
//	refresh                立刻刷新订阅
//	settings [key=value …] 查看或修改设置
//	rules                  规则组
//	logs [n] [core]        最近 n 行服务 / 内核日志
//	diag                   诊断信息(JSON)
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// Name 用法提示里的程序名;InstallHint 服务没起来时的提示。
var (
	Name        = "godusevpn-cli"
	InstallHint = "服务未运行:先以管理员身份执行 godusevpn-svc install"
)

// Main 执行一条命令,返回进程退出码。args 不含程序名。
func Main(args []string) int {
	if len(args) < 1 {
		Usage()
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd, args := args[0], args[1:]
	var err error
	switch cmd {
	case "status":
		err = status(ctx)
	case "connect":
		var v ipc.StateView
		if err = call(ctx, ipc.MConnect, nil, &v); err == nil {
			fmt.Println("已发起连接;当前:", v.State.Status)
		}
	case "disconnect":
		var v ipc.StateView
		if err = call(ctx, ipc.MDisconnect, nil, &v); err == nil {
			fmt.Println("已断开")
		}
	case "mode":
		if len(args) != 1 {
			err = errors.New("用法: mode rule|global|direct")
			break
		}
		var v ipc.StateView
		if err = call(ctx, ipc.MSetMode, map[string]string{"mode": args[0]}, &v); err == nil {
			fmt.Println("模式:", v.Mode)
		}
	case "nodes":
		var v ipc.StateView
		if err = call(ctx, ipc.MGetState, nil, &v); err == nil {
			for _, n := range v.Nodes {
				mark := "  "
				if n == v.Node {
					mark = "* "
				}
				ms := ""
				if d, ok := v.Delays[n]; ok {
					if d < 0 {
						ms = "  不通"
					} else {
						ms = fmt.Sprintf("  %d ms", d)
					}
				}
				fmt.Println(mark + n + ms)
			}
		}
	case "select":
		if len(args) != 1 {
			err = errors.New("用法: select <节点名|auto>")
			break
		}
		var v ipc.StateView
		if err = call(ctx, ipc.MSelectNode, map[string]string{"tag": args[0]}, &v); err == nil {
			fmt.Println("当前节点:", v.Node)
		}
	case "test":
		if len(args) == 1 && args[0] == "all" {
			var res map[string]int
			if err = call(ctx, ipc.MProbeNodes, nil, &res); err == nil {
				for k, ms := range res {
					if ms < 0 {
						fmt.Printf("%s: 不通\n", k)
					} else {
						fmt.Printf("%s: %d ms\n", k, ms)
					}
				}
			}
			break
		}
		tag := "proxy"
		if len(args) == 1 {
			tag = args[0]
		}
		var r struct {
			Tag string `json:"tag"`
			Ms  int    `json:"ms"`
		}
		if err = call(ctx, ipc.MTestLatency, map[string]string{"tag": tag}, &r); err == nil {
			fmt.Printf("%s: %d ms\n", r.Tag, r.Ms)
		}
	case "profile":
		var p *ipc.ProfileView
		if len(args) == 1 {
			err = call(ctx, ipc.MSetProfileURL, map[string]string{"url": args[0]}, &p)
		} else {
			err = call(ctx, ipc.MGetProfile, nil, &p)
		}
		if err == nil {
			printProfile(p)
		}
	case "refresh":
		var p *ipc.ProfileView
		if err = call(ctx, ipc.MRefreshProfile, nil, &p); err == nil {
			printProfile(p)
		}
	case "settings":
		var s settings.Settings
		if err = call(ctx, ipc.MGetSettings, nil, &s); err != nil {
			break
		}
		if len(args) > 0 {
			for _, kv := range args {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					err = fmt.Errorf("参数需为 key=value: %s", kv)
					break
				}
				if s, err = s.Apply(k, v); err != nil {
					break
				}
			}
			if err != nil {
				break
			}
			err = call(ctx, ipc.MSetSettings, s, &s)
		}
		if err == nil {
			s.WebPassword = "" // 哈希也不用给人看
			b, _ := json.MarshalIndent(s, "", "  ")
			fmt.Println(string(b))
		}
	case "rules":
		var s settings.Settings
		if err = call(ctx, ipc.MGetSettings, nil, &s); err != nil {
			break
		}
		if len(s.RuleGroups) == 0 {
			fmt.Println("没有自定义规则组(内置默认:私网直连、国内直连、其余走代理)")
		}
		for i, g := range s.RuleGroups {
			on := "开"
			if !g.Enabled {
				on = "关"
			}
			fmt.Printf("%d. %s [%s] → %s(%d 条)\n", i+1, g.Name, on, g.Outbound, len(g.Rules))
			for _, r := range g.Rules {
				fmt.Printf("     %-15s %s\n", r.Type, r.Value)
			}
		}
	case "devices":
		var list []ipc.DeviceView
		if err = call(ctx, ipc.MGetDevices, nil, &list); err != nil {
			break
		}
		if len(list) == 0 {
			fmt.Println("没有发现局域网设备(网关模式下把设备的网关指向本机)")
		}
		for _, d := range list {
			on := "离线"
			if d.Online {
				on = "在线"
			}
			mode := d.Mode
			if mode == "" {
				mode = "跟随规则"
			}
			fmt.Printf("%-16s %-16s %s  %s  %s\n", d.MAC, d.IP, on, mode, d.Name)
		}
	case "device":
		if len(args) < 2 {
			err = errors.New("用法: device <MAC> <follow|proxy|direct|reject> [名字]")
			break
		}
		mode := args[1]
		if mode == "follow" {
			mode = ""
		}
		name := ""
		if len(args) > 2 {
			name = strings.Join(args[2:], " ")
		}
		var list []ipc.DeviceView
		if err = call(ctx, ipc.MSetDevice, map[string]string{"mac": args[0], "mode": mode, "name": name}, &list); err == nil {
			fmt.Println("已保存")
		}
	case "logs":
		n, core := 100, false
		for _, a := range args {
			if a == "core" {
				core = true
			} else if v, e := strconv.Atoi(a); e == nil {
				n = v
			}
		}
		var lines []string
		if err = call(ctx, ipc.MGetLogs, map[string]any{"lines": n, "core": core}, &lines); err == nil {
			for _, l := range lines {
				fmt.Println(l)
			}
		}
	case "diag":
		var out map[string]any
		if err = call(ctx, ipc.MDiagnose, nil, &out); err == nil {
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
		}
	case "version", "-v", "--version":
		fmt.Println(buildinfo.DisplayName, buildinfo.Version)
	default:
		Usage()
		return 2
	}
	if err != nil {
		if errors.Is(err, ipc.ErrNoService) {
			fmt.Fprintln(os.Stderr, InstallHint)
		} else {
			fmt.Fprintln(os.Stderr, "失败:", err)
		}
		return 1
	}
	return 0
}

func call(ctx context.Context, method string, params, result any) error {
	return ipc.Call(ctx, method, params, result)
}

func status(ctx context.Context) error {
	var v ipc.StateView
	if err := call(ctx, ipc.MGetState, nil, &v); err != nil {
		return err
	}
	fmt.Printf("%s %s(服务 v%s)\n", buildinfo.DisplayName, buildinfo.Version, v.Version)
	st := string(v.State.Status)
	if v.State.Error != "" {
		st += fmt.Sprintf("  [%s] %s(第 %d 次重试)", v.State.Code, v.State.Error, v.State.Retries)
	}
	fmt.Println("状态:  ", st, map[bool]string{true: " (自动重连中)", false: ""}[v.State.Wanted && v.State.Status != "connected"])
	if v.Uptime > 0 {
		fmt.Println("已连接:", (time.Duration(v.Uptime) * time.Second).String())
	}
	fmt.Println("模式:  ", v.Mode)
	fmt.Println("节点:  ", v.Node, fmt.Sprintf("(共 %d 个)", len(v.Nodes)))
	s := v.Settings
	fmt.Printf("设置:   TUN=%v(%s, strict=%v) 混合端口=%d DoH=%s/%s fake-ip=%v IPv6=%v\n", s.TUN, s.TUNStack, s.StrictRoute, s.MixedPort, s.RemoteDNS, s.LocalDNS, s.FakeIP, s.IPv6)
	if s.WebListen != "" {
		fmt.Println("面板:   http://" + s.WebListen + "/")
	}
	printProfile(v.Profile)
	return nil
}

func printProfile(p *ipc.ProfileView) {
	if p == nil {
		fmt.Println("订阅:   (未设置)")
		return
	}
	fmt.Printf("订阅:   %s  %d 个节点  更新于 %s\n", p.Title, p.NodeCount, time.Unix(p.FetchedAt, 0).Format("01-02 15:04"))
	if p.Usage.Total > 0 || p.Usage.Expire > 0 {
		used := float64(p.Usage.Upload+p.Usage.Download) / (1 << 30)
		total := "不限"
		if p.Usage.Total > 0 {
			total = fmt.Sprintf("%.1f GB", float64(p.Usage.Total)/(1<<30))
		}
		exp := "不限"
		if p.Usage.Expire > 0 {
			exp = time.Unix(p.Usage.Expire, 0).Format("2006-01-02")
		}
		fmt.Printf("用量:   %.2f GB / %s  到期 %s\n", used, total, exp)
	}
}

// Usage 打印用法。
func Usage() {
	fmt.Println(buildinfo.DisplayName, buildinfo.Version)
	fmt.Println("用法: " + Name + " status | connect | disconnect | mode <rule|global|direct> | nodes | select <节点> | test [节点|all] | profile [地址] | refresh | settings [k=v …] | rules | devices | device <MAC> <follow|proxy|direct|reject> [名字] | logs [n] [core] | diag")
}
