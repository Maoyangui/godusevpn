package logx

import (
	"strings"
	"sync"
)

const ringLines = 2000 // 和日志页一次能要的行数一样

// Ring 只放内存的日志:最多留 ringLines 行,进程一退就没了。零值可用。
type Ring struct {
	mu    sync.Mutex
	lines []string
	start int
}

// Add 记一行,满了挤掉最早的。
func (r *Ring) Add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.lines) < ringLines {
		r.lines = append(r.lines, line)
		return
	}
	r.lines[r.start] = line
	r.start = (r.start + 1) % ringLines
}

// Tail 最近 n 行,从旧到新;返回的是副本。
func (r *Ring) Tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || len(r.lines) == 0 {
		return nil
	}
	if n > len(r.lines) {
		n = len(r.lines)
	}
	out := make([]string, 0, n)
	for i := len(r.lines) - n; i < len(r.lines); i++ {
		out = append(out, r.lines[(r.start+i)%len(r.lines)])
	}
	return out
}

// LevelOf 取出 Line 写的一行里的级别;格式不对返回空串。
func LevelOf(line string) string {
	if len(line) <= len(stamp)+1 || line[len(stamp)] != ' ' {
		return ""
	}
	rest := line[len(stamp)+1:]
	if i := strings.IndexByte(rest, ' '); i > 0 {
		return rest[:i]
	}
	return rest
}

// Serious 这个级别是不是警告及以上(级别写法见 core.Writer)。
func Serious(level string) bool {
	switch level {
	case "WARN", "ERROR", "FATAL", "PANIC":
		return true
	}
	return false
}
