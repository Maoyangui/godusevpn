// Package logx 一个不依赖第三方的滚动日志:写满 maxSize 就把 name → name.1 → name.2 … 顺次挪一位,只保留 keep 份;
// 另可设最长保留时间:当前文件里最早一行超过 maxAge 就滚动,滚出去的旧文件由 Prune 按修改时间清掉。
// 服务日志与内核日志各用一份,排障时"生成诊断包"把它们一起打包。
package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const stamp = "2006-01-02 15:04:05"

type Rotator struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	keep    int
	maxAge  time.Duration // 0 = 不按时间滚动
	f       *os.File
	size    int64
	started time.Time // 当前文件最早一行的时间
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
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.open(); err != nil {
		return 0, err
	}
	if r.size+int64(len(p)) > r.maxSize || (r.maxAge > 0 && r.size > 0 && time.Since(r.started) > r.maxAge) {
		r.rotate()
		if err := r.open(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

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
