package daemon

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/logx"
	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/redact"
)

func cmdOut(name string, args ...string) string {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("(%s %s: %v)\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// 诊断:命令行 diag(MDiagnose)与界面的「导出诊断包」(MExportDiag)用同一套打码(internal/redact):
// 凭据字段整值打码,地址只留协议与主机,订阅令牌在别处出现也盖掉;诊断包里的系统网络信息另把公网 IP、MAC、主机名打码;
// 内核日志里的访问目标(域名、公网地址)只留顶级域 / 前缀。

// diagMasker 这一次诊断输出用的打码器,带上这台设备上每条订阅地址里的令牌。
func (d *Daemon) diagMasker() *redact.Masker {
	var urls []string
	for _, p := range d.getSettings().Profiles {
		urls = append(urls, p.URL)
	}
	return redact.NewMasker(urls...)
}

// diagTree 先序列化成独立的 JSON 树再打码:状态里的结构体与切片可能和运行中的设置共用底层数据,
// 就地改会把真实的订阅链接改坏(0.6.0-a2 出过:之后刷新全是 404)。
func diagTree(m *redact.Masker, v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var detached any
	if err := json.Unmarshal(b, &detached); err != nil {
		return nil, err
	}
	return m.Tree(detached), nil
}

// diagInfo 两处共用的概况:版本、时间、状态(含设置)、当前订阅的节点地址。
// servers 是"地址:端口",不算凭据,deploy/*-test.sh 靠它从 diag 输出里找节点。
func (d *Daemon) diagInfo() map[string]any {
	info := map[string]any{"version": buildinfo.Version, "os": runtime.GOOS + "/" + runtime.GOARCH, "time": time.Now().Format(time.RFC3339), "state": d.stateView()}
	if _, p := d.activeProfile(); p != nil {
		info["servers"] = p.Servers()
	}
	return info
}

// diagnose 命令行 diag 的输出(另带两份日志各最后 100 行)。
func (d *Daemon) diagnose() (any, error) {
	info := d.diagInfo()
	info["dataDir"] = paths.DataDir()
	info["serviceLog"] = logx.Tail(d.log.Path(), 100)
	core := logx.Tail(d.coreLog.Path(), 100)
	for i, l := range core {
		core[i] = redact.Destinations(l) // 访问过的域名与公网地址合起来就是浏览记录
	}
	info["coreLog"] = core
	return diagTree(d.diagMasker(), info)
}

// exportDiag 生成 zip,返回路径。
func (d *Daemon) exportDiag() (string, error) {
	dir := filepath.Join(paths.DataDir(), "diag")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "godusevpn-diag-"+time.Now().Format("20060102-150405")+".zip")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	m := d.diagMasker()
	add := func(name, content string) {
		w, err := zw.Create(name)
		if err == nil {
			_, _ = w.Write([]byte(m.Text(content)))
		}
	}
	info, err := diagTree(m, d.diagInfo())
	if err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return "", err
	}
	add("info.json", string(b))
	if raw, err := os.ReadFile(paths.Config()); err == nil {
		var cfg any
		if json.Unmarshal(raw, &cfg) == nil {
			rb, _ := json.MarshalIndent(m.Tree(cfg), "", "  ")
			add("config.redacted.json", string(rb))
		}
	}
	add("service.log", strings.Join(logx.Tail(d.log.Path(), 500), "\n"))
	add("core.log", redact.Destinations(strings.Join(logx.Tail(d.coreLog.Path(), 500), "\n")))
	// 崩溃记录(Windows 服务 / Linux / macOS 的 CaptureCrashes,Android 上 Go / Kotlin 侧)。.1 是启动时超过 1MB
	// 挪走的那份 —— 轮转恰恰发生在一次崩溃把文件推过 1MB 之后,现场在那里。
	for _, name := range []string{"crash.log", "crash.log.1"} {
		if s := crashExcerpt(filepath.Join(paths.Logs(), name)); s != "" {
			add(name, s)
		}
	}
	sysDiag(func(name, content string) { add(name, redact.Net(content)) })
	if err := zw.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// crashExcerpt 崩溃记录摘要。每段(以"== … 启动"抬头分开)的原因行在最前面("fatal error: …"、"Exception 0x…"、
// "panic: …"),后面跟着全部 goroutine 的栈,动辄上千行;只取整个文件的最后几百行会把原因截掉。
// 只挑有内容的段(抬头以外还有非空行),取最后 3 段,每段超过 200 行就留头 150 行、尾 50 行:每次正常启动都会写
// 一行抬头,按段数取的话,崩溃之后服务被自动拉起、再开两次机,崩溃那段就被只有抬头的段挤出去了。
// 最后一段有内容的之后还有几次启动,记一行。没有抬头的(Android 那份)照旧取最后 300 行。
func crashExcerpt(path string) string {
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	var starts []int
	for i, l := range lines {
		if strings.HasPrefix(l, "== ") {
			starts = append(starts, i)
		}
	}
	if len(starts) == 0 {
		if len(lines) > 300 {
			lines = lines[len(lines)-300:]
		}
		return strings.Join(lines, "\n")
	}
	if starts[0] > 0 {
		starts = append([]int{0}, starts...) // 第一个抬头之前的内容(轮转时被截在中间的那段)单算一段
	}
	type seg struct{ s, e int }
	var withContent []seg
	lastContent := -1
	for k, st := range starts {
		end := len(lines)
		if k+1 < len(starts) {
			end = starts[k+1]
		}
		for _, l := range lines[st:end] {
			if strings.TrimSpace(l) != "" && !strings.HasPrefix(l, "== ") {
				withContent = append(withContent, seg{st, end})
				lastContent = k
				break
			}
		}
	}
	if len(withContent) == 0 {
		// 全是启动抬头:没有崩溃记录。留最后一行抬头,看得出记录本身是开着的
		return lines[starts[len(starts)-1]] + "\n(没有崩溃记录)"
	}
	if len(withContent) > 3 {
		withContent = withContent[len(withContent)-3:]
	}
	var out []string
	for _, g := range withContent {
		blk := lines[g.s:g.e]
		if len(blk) > 200 {
			out = append(out, blk[:150]...)
			out = append(out, fmt.Sprintf("…(省略 %d 行)…", len(blk)-200))
			out = append(out, blk[len(blk)-50:]...)
		} else {
			out = append(out, blk...)
		}
	}
	if later := len(starts) - 1 - lastContent; later > 0 {
		out = append(out, fmt.Sprintf("…(之后又启动了 %d 次,没有崩溃记录;最后一次:%s)", later, lines[starts[len(starts)-1]]))
	}
	return strings.Join(out, "\n")
}
