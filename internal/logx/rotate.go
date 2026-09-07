// Package logx 一个不依赖第三方的滚动日志:写满 maxSize 就把 name → name.1 → name.2 … 顺次挪一位,只保留 keep 份。
// 服务日志与内核日志各用一份,排障时"生成诊断包"把它们一起打包。
package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type Rotator struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	keep    int
	f       *os.File
	size    int64
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

func (r *Rotator) open() error {
	if r.f != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, _ := f.Stat()
	r.f = f
	if st != nil {
		r.size = st.Size()
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
}

func (r *Rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(p)) > r.maxSize {
		r.rotate()
	}
	if err := r.open(); err != nil {
		return 0, err
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

// Printf 带时间与级别的一行。
func (r *Rotator) Printf(level, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	_, _ = r.Write([]byte(time.Now().Format("2006-01-02 15:04:05") + " " + level + " " + msg + "\n"))
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
