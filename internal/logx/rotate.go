// Package logx 一个不依赖第三方的滚动日志:写满 maxSize 就把 name → name.1 → name.2 … 顺次挪一位,只保留 keep 份;
// 另可设最长保留时间:当前文件里最早一行超过 maxAge 就滚动,滚出去的旧文件由 Prune 按修改时间清掉。
// 服务日志与内核日志各用一份,排障时"生成诊断包"把它们一起打包。
//
// 写进去的每一行都先过 redact.Text:拉订阅失败的错误里带着完整订阅地址(路径就是令牌),
// 日志页、日志目录、诊断包都会把它给人看。打码只在这一处做,调用方不用各自操心。
package logx

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Maoyangui/godusevpn/internal/redact"
)

const stamp = "2006-01-02 15:04:05"

type Rotator struct {
	mu       sync.Mutex
	path     string
	maxSize  int64
	keep     int
	maxAge   time.Duration // 0 = 不按时间滚动
	f        *os.File
	size     int64
	started  time.Time // 当前文件最早一行的时间
	scrubbed bool      // 本进程已把老版本留下的几份过过一遍
}

// New 返回一个滚动写入器;文件在第一次写入时才创建。
func New(path string, maxSize int64, keep int) *Rotator {
	if maxSize <= 0 {
		maxSize = 5 << 20
	}
	if keep < 1 {
		keep = 3
	}
	return &Rotator{path: path, maxSize: maxSize, keep: keep}
}

// SetMaxAge 设最长保留时间(0 = 一直保留)。
func (r *Rotator) SetMaxAge(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxAge = d
}

func (r *Rotator) open() error {
	if r.f != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	if !r.scrubbed {
		r.scrubbed = true
		for i := 0; i <= r.keep; i++ {
			p := r.path
			if i > 0 {
				p += "." + strconv.Itoa(i)
			}
			scrub(p)
		}
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, _ := f.Stat()
	r.f = f
	r.size = 0
	r.started = time.Now()
	if st != nil && st.Size() > 0 {
		r.size = st.Size()
		// 每行以时间戳开头,读第一行就知道这个文件从什么时候开始的
		head := make([]byte, len(stamp))
		if n, _ := f.ReadAt(head, 0); n == len(stamp) {
			if ts, err := time.ParseInLocation(stamp, string(head), time.Local); err == nil {
				r.started = ts
			}
		}
	}
	return nil
}

func (r *Rotator) rotate() {
	if r.f != nil {
		r.f.Close()
		r.f = nil
	}
	os.Remove(r.path + "." + strconv.Itoa(r.keep))
	for i := r.keep - 1; i >= 1; i-- {
		os.Rename(r.path+"."+strconv.Itoa(i), r.path+"."+strconv.Itoa(i+1))
	}
	os.Rename(r.path, r.path+".1")
	r.size = 0
	r.started = time.Now()
}

func (r *Rotator) Write(p []byte) (int, error) {
	line := []byte(redact.Text(string(p)))
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.open(); err != nil {
		return 0, err
	}
	if r.size+int64(len(line)) > r.maxSize || (r.maxAge > 0 && r.size > 0 && time.Since(r.started) > r.maxAge) {
		r.rotate()
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(line)
	r.size += int64(n)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// scrub v0.7.5 及以前写下的日志没打过码(拉订阅失败的那几行带着完整订阅地址)。每个进程第一次打开时把现有的
// 几份过一遍:先只读着找,有要改的行才重写;修改时间照旧,免得按时间清理的旧文件又多留几天。
func scrub(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	st, err := f.Stat()
	dirty := false
	if err == nil {
		dirty = eachLine(f, func(l string) bool { return redact.Text(l) != l })
	}
	f.Close()
	if !dirty {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var out bytes.Buffer
	for _, l := range strings.SplitAfter(string(b), "\n") {
		if mayLeak(l) {
			l = redact.Text(l)
		}
		out.WriteString(l)
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, out.Bytes(), 0o600) != nil || os.Rename(tmp, path) != nil {
		os.Remove(tmp)
		return
	}
	_ = os.Chtimes(path, st.ModTime(), st.ModTime())
}

// eachLine 逐行读(不整份读进内存:内核日志一份 5MB),碰到可能带地址、且打码后会变的行就返回 true。
func eachLine(rd io.Reader, changes func(string) bool) bool {
	br := bufio.NewReader(rd)
	for {
		l, err := br.ReadString('\n')
		if mayLeak(l) && changes(l) {
			return true
		}
		if err != nil {
			return false
		}
	}
}

// mayLeak 老日志里泄露的只有地址(url.Error 的文本);没有 :// 也没有 /sub/ 的行不用过正则。
func mayLeak(l string) bool { return strings.Contains(l, "://") || strings.Contains(l, "/sub/") }

// Printf 带时间与级别的一行。
func (r *Rotator) Printf(level, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	_, _ = r.Write([]byte(time.Now().Format(stamp) + " " + level + " " + msg + "\n"))
}

func (r *Rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// Path 当前文件路径(诊断包用)。
func (r *Rotator) Path() string { return r.path }

// Prune 删掉目录里修改时间早于 maxAge 的滚动日志(name.N)与其它普通文件;正在写的 *.log 不动。
// maxAge <= 0 什么都不做。返回删掉的个数。
func Prune(dir string, maxAge time.Duration) int {
	if maxAge <= 0 {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cut := time.Now().Add(-maxAge)
	n := 0
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".log") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cut) {
			continue
		}
		if os.Remove(filepath.Join(dir, e.Name())) == nil {
			n++
		}
	}
	return n
}

// Tail 读取最近 n 行(排障与界面日志页用);文件不存在返回空。
func Tail(path string, n int) []string {
	b, err := os.ReadFile(path)
	if err != nil || n <= 0 {
		return nil
	}
	lines := splitLines(string(b))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
