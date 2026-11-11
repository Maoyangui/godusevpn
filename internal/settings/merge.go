package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// 几个进程会写同一份设置:守护进程(界面、网页面板、命令行都经控制口找它)、服务没跑时的命令行(passwd)、
// 「恢复网络」的离线兜底、安装程序。以前各自"读出整份、改几项、整份存回去",两边一交错,后存的把先存的那几项盖回旧值。
// 现在:
//   - 写文件一律持着同一把跨进程的锁(lockFile),读改写在锁里做完(Update);
//   - 守护进程手里一直有一份设置,存之前看文件是不是还是它上次读到 / 写下的那样,不是(别的进程这期间写过)就按字段
//     合并:别人改的那几项留着,自己这次改的那几项盖上去(SaveChecked);
//   - 经控制口整份存设置的,带着它读到的那一版(Rev),守护进程只把相对那一版改了的字段合并到此刻的设置上(Merge3)。

// Merge3 三方合并:base 是 next 的出发点,cur 是此刻的设置。在 cur 上把 next 相对 base 改了的顶层字段(JSON 键)换成 next 的,
// 别的保持 cur —— base 之后别人改的不被盖掉;两边改了同一项时 next 的算数(后存的为准)。
// 不经 JSON 的字段(SelectedNode)和 Rev 保持 cur 的。
func Merge3(base, cur, next Settings) (Settings, error) {
	base.Rev, cur.Rev, next.Rev = 0, 0, 0
	b, err := fields(base)
	if err != nil {
		return cur, err
	}
	n, err := fields(next)
	if err != nil {
		return cur, err
	}
	changed := map[string]json.RawMessage{} // 值为 nil:next 里没有这个键了(omitempty 的字段改成了空)
	for k, v := range n {
		if !bytes.Equal(v, b[k]) {
			changed[k] = v
		}
	}
	for k := range b {
		if _, ok := n[k]; !ok {
			changed[k] = nil
		}
	}
	out := cur.Clone()
	if len(changed) == 0 {
		return out, nil
	}
	c, err := fields(cur)
	if err != nil {
		return cur, err
	}
	for k, v := range changed {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return cur, err
	}
	// 解到一份清空了切片的副本上:JSON 解切片是按下标覆盖,旧的长切片会留尾巴
	var m Settings
	if err := json.Unmarshal(raw, &m); err != nil {
		return cur, err
	}
	m.SelectedNode = cur.SelectedNode
	return m, nil
}

// fields 设置按顶层 JSON 键拆开。
func fields(s Settings) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	return m, json.Unmarshal(raw, &m)
}

// Update 在锁里读出设置、交给 fn 改、存回去:服务没跑时直接改文件的那几条路(命令行 passwd、「恢复网络」的离线兜底、安装)用它。
// 文件读不出来时不改(存回去等于把整份覆盖成默认值),返回读的错误;fn 返回错误也不存。
func Update(path string, fn func(s *Settings) error) (Settings, error) {
	unlock, err := lockFile(path)
	if err != nil {
		return Settings{}, err
	}
	defer unlock()
	s, _, err := LoadRaw(path)
	if err != nil {
		return s, err
	}
	if err := fn(&s); err != nil {
		return s, err
	}
	if err := s.Validate(); err != nil {
		return s, err
	}
	_, err = s.write(path)
	return s, err
}

// SaveChecked 守护进程存设置。base / baseRaw 是它上次读到或写下的那份(设置和原始字节),baseOK 为假表示那次没读出来
// (手上是默认值,这时不合并,整份覆盖 —— 覆盖前的另存由调用方做)。存之前在锁里看文件:还是 baseRaw 就照存 s;
// 变了(别的进程这期间写过)就在文件现在的内容上合并 s 相对 base 改了的字段(见 Merge3),存合并后的那份。
// 返回实际存下的设置和字节,调用方拿它们当下一次的 base。文件现在的内容读不出来就照存 s。
func SaveChecked(path string, base Settings, baseRaw []byte, baseOK bool, s Settings) (Settings, []byte, error) {
	unlock, err := lockFile(path)
	if err != nil {
		return s, nil, err
	}
	defer unlock()
	if baseOK {
		cur, rerr := os.ReadFile(path)
		if rerr == nil && !bytes.Equal(cur, baseRaw) {
			if disk, perr := parse(cur); perr == nil {
				m, merr := Merge3(base, disk, s)
				if merr == nil {
					// 选择没被别人改过时,SelectedNode(不经 JSON)用这次要存的
					if disk.Selected == base.Selected {
						m.SelectedNode = s.SelectedNode
					}
					if m.Validate() == nil {
						s = m
					}
				}
			}
		}
	}
	raw, err := s.write(path)
	return s, raw, err
}

// lockFile 写设置文件的跨进程锁:<path>.lock 用 O_EXCL 建出来就算拿到,删掉就是放开。
// 持锁的只做一次读改写(毫秒级);锁文件放了 staleLock 还在,就是持锁的进程没放就没了(崩溃、被杀),当作已放开。
// 等不到(lockWait)报错,不硬写。
func lockFile(path string) (unlock func(), err error) {
	lp := path + ".lock"
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(lp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(lp) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			if errors.Is(err, os.ErrNotExist) { // 目录还没建:没有别人在写,也没有文件可写,交给写的那一步去报
				return func() {}, nil
			}
			return nil, fmt.Errorf("锁设置文件: %w", err)
		}
		if fi, serr := os.Stat(lp); serr == nil && time.Since(fi.ModTime()) > staleLock {
			_ = os.Remove(lp)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errors.New("设置文件正被别的进程写着,稍后再试")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var (
	lockWait  = 15 * time.Second
	staleLock = 10 * time.Second
)
