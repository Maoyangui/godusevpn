// Package cli 排障与脚本用的命令行,Windows 的 godusevpn-cli 与 Linux 的 godusevpn 共用,都走本机控制口。
//
//	status                 当前状态、模式、节点、订阅
//	connect / disconnect
//	mode rule|global|direct
//	nodes                  节点列表(当前项带 *),订阅里有线路组时后面再列线路组与成员
//	select <节点名|线路组名|auto>
//	test [节点名]          经该节点(默认当前 proxy 组)测延迟
//	profile [订阅地址]     查看或设置订阅
//	refresh                立刻刷新订阅
//	settings [key=value …] 查看或修改设置;lineAuto=on|off 走「线路自动」开关(顺带换算当前选择)
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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/guardfix"
	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// Name 用法提示里的程序名;InstallHint 服务没起来时的提示。
var (
	Name        = "godusevpn-cli"
	InstallHint = "服务未运行:先以管理员身份执行 godusevpn-svc install"
)

// callTimeout 一条命令最多等多久:select、mode、settings 这些要备新配置,守护进程那边最长要好几分钟(见 ipc.SlowCallTimeout)。
const callTimeout = ipc.SlowCallTimeout

// Main 执行一条命令,返回进程退出码。args 不含程序名。
func Main(args []string) int {
	if len(args) < 1 {
		Usage()
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
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
			printGroups(v)
		}
	case "select":
		if len(args) != 1 {
			err = errors.New("用法: select <节点名|线路组名|auto>")
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
	case "node":
		err = nodeCmd(ctx, args)
	case "refresh":
		var list []ipc.ProfileView // 服务返回的是全部订阅,打印当前这条
		if err = call(ctx, ipc.MRefreshProfile, nil, &list); err == nil {
			var cur *ipc.ProfileView
			for i := range list {
				if list[i].Active {
					cur = &list[i]
				}
			}
			printProfile(cur)
		}
	case "settings":
		var s settings.Settings
		if err = call(ctx, ipc.MGetSettings, nil, &s); err != nil {
			break
		}
		if len(args) > 0 {
			// lineAuto 只归「线路自动」开关管(保存设置时服务沿用原值):拦下来单独调,其余照常一起保存
			rest, lineAuto, lerr := takeLineAuto(args)
			if err = lerr; err != nil {
				break
			}
			for _, kv := range rest {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					err = fmt.Errorf("参数需为 key=value: %s", kv)
					break
				}
				if k == "webPassword" || k == "rev" {
					// 密码只经 passwd 改(服务那边加盐哈希、只动这一项);rev 是读设置时带的版本号,存回去按它合并别处这期间的修改
					err = fmt.Errorf("%s 不能这样改(改面板密码用 passwd)", k)
					break
				}
				if s, err = s.Apply(k, v); err != nil {
					break
				}
			}
			if err != nil {
				break
			}
			if len(rest) > 0 {
				if err = call(ctx, ipc.MSetSettings, s, &s); err != nil {
					break
				}
			}
			if lineAuto != nil {
				var v ipc.StateView
				if err = call(ctx, ipc.MSetLineAuto, map[string]bool{"on": *lineAuto}, &v); err != nil {
					break
				}
				s = v.Settings
			}
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
			err = errors.New("用法: device <MAC> <follow|proxy|direct|reject> [名字](全局模式下防流量逃逸开着时 direct 不生效)")
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
	case "guard": // 「恢复网络」:守护进程起不来、闸还在,手动撤掉(要 root / 管理员)
		sub := ""
		if len(args) > 0 {
			sub = args[0]
		}
		switch sub {
		case "clear":
			text, ok := guardfix.Clear()
			fmt.Println(text)
			if !ok {
				err = errors.New("需要 root / 管理员身份")
			}
		case "status":
			var n int
			if n, err = guardfix.Status(); err == nil {
				if n == 0 {
					fmt.Println("闸:没开(直连不受限)")
				} else {
					fmt.Printf("闸:开着(%d 条规则;隧道以外的流量一律拦下)\n", n)
				}
			}
		default:
			err = errors.New("用法: guard clear|status")
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
		} else if errors.Is(err, ipc.ErrNoPermission) {
			fmt.Fprintln(os.Stderr, err.Error()) // 各平台的提示不一样(Windows 是"账户没登记"),原样打出来
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
	node := v.Node
	if v.AutoNow != "" && v.AutoNow != v.Node {
		node += "(实际在用 " + v.AutoNow + ")"
	}
	fmt.Println("节点:  ", node, fmt.Sprintf("(共 %d 个)", len(v.Nodes)))
	if v.SelectedV6 {
		fmt.Println("提示:  ", v6Hint(v))
	}
	s := v.Settings
	fmt.Printf("设置:   TUN=%v(%s, strict=%v) 混合端口=%d DoH=%s/%s fake-ip=%v IPv6=%v\n", s.TUN, s.TUNStack, s.StrictRoute, s.MixedPort, s.RemoteDNS, s.LocalDNS, s.FakeIP, s.IPv6)
	if s.WebListen != "" {
		fmt.Println("面板:   http://" + s.WebListen + "/")
	}
	printProfile(v.Profile)
	if tv := v.Tunnel; tv != nil && (tv.Rebuilds > 0 || tv.Sick > 0) {
		// 会话看护的账:sick 在涨说明会话在坏、看护在修;一直不涨而网页打不开就不是这一层的事
		last := ""
		if tv.LastAt > 0 {
			last = fmt.Sprintf("  最近 %s %s:%s", time.Unix(tv.LastAt, 0).Format("01-02 15:04:05"), tv.LastNode, tv.LastReason)
		}
		fmt.Printf("隧道:   判废 %d 次,重建 %d 次%s\n", tv.Sick, tv.Rebuilds, last)
	}
	return nil
}

// printGroups nodes 命令的后半:订阅里的线路组和各自的成员(当前选中的带 *)。没有就什么都不打。
func printGroups(v ipc.StateView) {
	if len(v.Groups) == 0 {
		return
	}
	on := "关"
	if v.Settings.LineAuto {
		on = "开"
	}
	fmt.Printf("线路组(线路自动:%s;select <线路组名> 就在组里自动用最快的那台):\n", on)
	for _, g := range v.Groups {
		mark := "  "
		if g.Tag == v.Node || g.Tag == v.Settings.Selected {
			mark = "* "
		}
		fmt.Println(mark + g.Tag + ":" + strings.Join(g.Members, " / "))
	}
}

// takeLineAuto 从 settings 的参数里挑出 lineAuto=on|off(它要单独调 SetLineAuto),返回剩下的参数。
func takeLineAuto(args []string) (rest []string, on *bool, err error) {
	for _, kv := range args {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k != "lineAuto" {
			rest = append(rest, kv)
			continue
		}
		var b bool
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "on", "true", "yes", "1":
			b = true
		case "off", "false", "no", "0":
		default:
			return nil, nil, fmt.Errorf("lineAuto 的值只能是 on 或 off: %s", v)
		}
		on = &b
	}
	return rest, on, nil
}

func printProfile(p *ipc.ProfileView) {
	if p == nil {
		fmt.Println("订阅:   (未设置)")
		return
	}
	name := p.Title
	if name == "" {
		name = p.Name
	}
	if p.Manual {
		fmt.Printf("订阅:   %s  %d 个节点(手动添加)\n", name, p.NodeCount)
		return
	}
	fmt.Printf("订阅:   %s  %d 个节点  更新于 %s\n", name, p.NodeCount, time.Unix(p.FetchedAt, 0).Format("01-02 15:04"))
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
// nodeCmd 手动节点:node add <分享链接...> | node list | node rm <名字>
func nodeCmd(ctx context.Context, args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "add":
		var parsed ipc.ParsedNodes
		if err := call(ctx, ipc.MParseNodeLinks, map[string]string{"text": strings.Join(args[1:], "\n")}, &parsed); err != nil {
			return err
		}
		for _, e := range parsed.Errors {
			fmt.Printf("第 %d 条没加上:%s\n", e.Line, e.Msg)
		}
		if len(parsed.Nodes) == 0 {
			return errors.New("没有能添加的节点")
		}
		var list []ipc.ProfileView
		if err := call(ctx, ipc.MSaveManualNodes, map[string]any{"nodes": parsed.Nodes}, &list); err != nil {
			return err
		}
		fmt.Printf("已添加 %d 个节点\n", len(parsed.Nodes))
		return nil
	case "list", "":
		var view ipc.ManualNodes
		if err := call(ctx, ipc.MGetManualNodes, nil, &view); err != nil {
			return err
		}
		for _, raw := range view.Nodes {
			var n struct {
				Tag    string `json:"tag"`
				Type   string `json:"type"`
				Server string `json:"server"`
				Port   int    `json:"server_port"`
			}
			if json.Unmarshal(raw, &n) == nil {
				fmt.Printf("%s  %s  %s:%d\n", n.Tag, n.Type, n.Server, n.Port)
			}
		}
		if len(view.Nodes) == 0 {
			fmt.Println("还没有手动节点")
		}
		return nil
	case "rm":
		if len(args) < 2 {
			return errors.New("用法: " + Name + " node rm <节点名>")
		}
		var list []ipc.ProfileView
		return call(ctx, ipc.MRemoveManualNode, map[string]string{"tag": strings.Join(args[1:], " ")}, &list)
	}
	return errors.New("用法: " + Name + " node add <分享链接...> | node list | node rm <节点名>")
}

func Usage() {
	fmt.Println(buildinfo.DisplayName, buildinfo.Version)
	fmt.Println("用法: " + Name + " status | connect | disconnect | mode <rule|global|direct> | nodes | node add <分享链接...> | node list | node rm <节点名> | select <节点|线路组> | test [节点|all] | profile [地址] | refresh | settings [k=v …] | rules | devices | device <MAC> <follow|proxy|direct|reject> [名字] | logs [n] [core] | diag | guard clear|status")
}

// v6Hint 选的这一轮连不上、原因是只能走 IPv6 而「允许 IPv6」关着时的提示:点名是哪个节点 / 哪条线路,说清楚去哪儿改。
func v6Hint(v ipc.StateView) string {
	sel := v.Settings.Selected
	what := "「" + sel + "」只能走 IPv6"
	if sel == "" || !slices.Contains(v.Nodes, sel) && !isGroup(v, sel) {
		what = "订阅里的节点全都只能走 IPv6" // 自动选择(选的已经不在订阅里也落在自动选择上)
	}
	if isGroup(v, sel) {
		what = "线路「" + sel + "」的服务器全都只能走 IPv6"
	}
	return what + ",而「允许 IPv6」关着,连不上:要用就打开 IPv6(settings ipv6=on,界面里是 设置 → 隐私),或者换别的节点"
}

func isGroup(v ipc.StateView, tag string) bool {
	for _, g := range v.Groups {
		if g.Tag == tag {
			return true
		}
	}
	return false
}
