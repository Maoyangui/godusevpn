// godusevpn-cli 佛跳墙 的命令行:排障与脚本用,和托盘客户端走同一条控制管道。
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
//	logs [n] [core]        最近 n 行服务 / 内核日志
//	diag                   诊断信息(JSON)
package main

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

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	args := os.Args[2:]
	var err error
	switch os.Args[1] {
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
				fmt.Println(mark + n)
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
			b, _ := json.MarshalIndent(s, "", "  ")
			fmt.Println(string(b))
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
		fmt.Println(buildinfo.DisplayName, "cli", buildinfo.Version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, ipc.ErrNoService) {
			fmt.Fprintln(os.Stderr, "服务未运行:先以管理员身份执行 godusevpn-svc install")
		} else {
			fmt.Fprintln(os.Stderr, "失败:", err)
		}
		os.Exit(1)
	}
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

func usage() {
	fmt.Println(buildinfo.DisplayName, "cli", buildinfo.Version)
	fmt.Println("用法: godusevpn-cli status | connect | disconnect | mode <rule|global|direct> | nodes | select <节点> | test [节点] | profile [地址] | refresh | settings [k=v …] | logs [n] [core] | diag")
}
