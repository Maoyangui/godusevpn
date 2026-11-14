package settings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// 三方合并:只把 next 相对 base 改了的字段换上去,base 之后别人改的留着;两边改同一项时 next 为准;切片变短不留尾巴。
func TestMerge3(t *testing.T) {
	base := Default()
	base.BypassApps = []string{"a.exe", "b.exe", "c.exe"}
	base.WebListen = "127.0.0.1:9800"
	cur := base.Clone()
	cur.AdBlock = !base.AdBlock // 别人改的
	cur.LogDays = 3             // 两边都改的
	cur.SelectedNode = "hk-1"
	next := base.Clone()
	next.LogDays = 9
	next.BypassApps = []string{"x.exe"}
	next.WebListen = "" // 改成空(omitempty 之外的字段,照样算改)
	next.Rev = 77
	m, err := Merge3(base, cur, next)
	if err != nil {
		t.Fatal(err)
	}
	if m.AdBlock != cur.AdBlock || m.LogDays != 9 || strings.Join(m.BypassApps, ",") != "x.exe" || m.WebListen != "" {
		t.Fatalf("合并不对: adBlock=%v logDays=%d bypass=%v webListen=%q", m.AdBlock, m.LogDays, m.BypassApps, m.WebListen)
	}
	if m.SelectedNode != "hk-1" || m.Rev != 0 {
		t.Fatalf("不经 JSON 的字段要保持 cur 的、版本号不带进来: %q %d", m.SelectedNode, m.Rev)
	}
	// next 什么都没改:就是 cur
	same, err := Merge3(base, cur, base)
	if err != nil || same.LogDays != 3 || same.AdBlock != cur.AdBlock {
		t.Fatalf("没改就该是 cur: %+v %v", same, err)
	}
}

// 版本号不落盘:文件里写了也不认。
func TestRevNotPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := Default()
	s.Rev = 5
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), `"rev"`) {
		t.Fatalf("版本号落盘了: %s", b)
	}
	_ = os.WriteFile(path, []byte(strings.Replace(string(b), "{", `{"rev": 9,`, 1)), 0o600)
	if got, err := Load(path); err != nil || got.Rev != 0 {
		t.Fatalf("文件里的版本号不该认: %d %v", got.Rev, err)
	}
}

// 好几个进程同时"读出来、改一项、存回去":在锁里做完,谁的都不丢。
func TestUpdateConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Default().Save(path); err != nil {
		t.Fatal(err)
	}
	const n = 30
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := Update(path, func(s *Settings) error {
				s.BypassApps = append(s.BypassApps, fmt.Sprintf("p%d.exe", i))
				return nil
			}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	s, err := Load(path)
	if err != nil || len(s.BypassApps) != n {
		t.Fatalf("并发改设置丢了: %d 条 %v", len(s.BypassApps), err)
	}
}

// Windows 上删掉的文件只要还有句柄开着(别的等锁的进程正 Stat 它、杀毒软件、索引服务),名字就处在"删除挂起":
// 这时 CreateFile(CREATE_NEW) 报的是 ERROR_ACCESS_DENIED,不是"已存在"。删锁时别人正开着它、改名替换时别的进程
// 正读着设置文件,同样报拒绝访问 / 共享冲突。这些都是等一下就好的。CI 的 Windows 上 TestUpdateConcurrent 就这样挂过:
// 30 个里有一个建锁拿到 "Access is denied" 直接报错,改的那一条丢了。
// 这里每次删锁之后的几毫秒里让建锁报拒绝访问,删锁、改名也隔几次失败一回:并发改设置不能报错、不能丢。
func TestUpdateConcurrentTransientErrors(t *testing.T) {
	denied := transientErr(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Default().Save(path); err != nil {
		t.Fatal(err)
	}
	origOpen, origRemove, origRename := openLockFile, removeFile, renameFile
	t.Cleanup(func() { openLockFile, removeFile, renameFile = origOpen, origRemove, origRename })
	var pendingUntil, removes, renames atomic.Int64
	removeFile = func(name string) error {
		if !strings.HasSuffix(name, ".lock") {
			return origRemove(name)
		}
		if removes.Add(1)%7 == 0 {
			return &os.PathError{Op: "remove", Path: name, Err: denied}
		}
		err := origRemove(name)
		pendingUntil.Store(time.Now().Add(3 * time.Millisecond).UnixNano())
		return err
	}
	openLockFile = func(name string) (*os.File, error) {
		if time.Now().UnixNano() < pendingUntil.Load() {
			return nil, &os.PathError{Op: "open", Path: name, Err: denied}
		}
		return origOpen(name)
	}
	renameFile = func(from, to string) error {
		if renames.Add(1)%5 == 0 {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: denied}
		}
		return origRename(from, to)
	}
	const n = 30
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := Update(path, func(s *Settings) error {
				s.BypassApps = append(s.BypassApps, fmt.Sprintf("p%d.exe", i))
				return nil
			}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	s, err := Load(path)
	if err != nil || len(s.BypassApps) != n {
		t.Fatalf("并发改设置丢了: %d 条 %v", len(s.BypassApps), err)
	}
	if _, err := os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("写完锁要放开: %v", err)
	}
	// 删锁失败一回就把锁留到 staleLock 才被别人清掉的话,30 个排队要等很久
	if d := time.Since(start); d > staleLock {
		t.Fatalf("删锁失败没有重试,锁被晾到过期才放开: %v", d)
	}
}

// 一直是瞬时错误(其实是真的没权限)时不能一直等:等 ephemeralWait 之后把真实的错误报出来,不说成"正被别的进程写着"。
// 不是瞬时的错误马上报。
func TestLockPersistentError(t *testing.T) {
	denied := transientErr(t)
	path := filepath.Join(t.TempDir(), "settings.json")
	origOpen, oldWait := openLockFile, ephemeralWait
	t.Cleanup(func() { openLockFile, ephemeralWait = origOpen, oldWait })
	ephemeralWait = 100 * time.Millisecond
	openLockFile = func(name string) (*os.File, error) {
		return nil, &os.PathError{Op: "open", Path: name, Err: denied}
	}
	start := time.Now()
	err := Default().Save(path)
	if err == nil || !errors.Is(err, denied) {
		t.Fatalf("一直拒绝访问要报出真实的错误: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("一直拒绝访问等太久了: %v", d)
	}
	other := errors.New("别的错误")
	openLockFile = func(name string) (*os.File, error) {
		return nil, &os.PathError{Op: "open", Path: name, Err: other}
	}
	start = time.Now()
	if err := Default().Save(path); !errors.Is(err, other) || time.Since(start) > time.Second {
		t.Fatalf("不是瞬时的错误要马上报: %v %v", err, time.Since(start))
	}
}

// 只有 Windows 的拒绝访问、共享冲突、锁冲突、删除挂起算瞬时;文件不存在、别的平台的 EACCES 不算。
func TestEphemeralErrors(t *testing.T) {
	wrap := func(e syscall.Errno) error { return &os.PathError{Op: "open", Path: "x", Err: e} }
	if runtime.GOOS == "windows" {
		for _, e := range []syscall.Errno{5, 32, 33, 303} { // ERROR_ACCESS_DENIED、SHARING_VIOLATION、LOCK_VIOLATION、DELETE_PENDING
			if !isEphemeral(wrap(e)) || !isEphemeral(&os.LinkError{Op: "rename", Err: e}) {
				t.Fatalf("Windows 错误 %d 应当算瞬时", e)
			}
		}
		if isEphemeral(wrap(2)) || isEphemeral(wrap(80)) { // ERROR_FILE_NOT_FOUND、ERROR_FILE_EXISTS
			t.Fatal("文件不存在 / 已存在不算瞬时")
		}
	} else if isEphemeral(wrap(syscall.EACCES)) || isEphemeral(wrap(syscall.EEXIST)) {
		t.Fatal("别的平台上删文件是原子的,EACCES 是真的没权限,不该重试")
	}
	if isEphemeral(nil) || isEphemeral(os.ErrExist) {
		t.Fatal("nil / 已存在不算瞬时")
	}
}

// transientErr 模拟 Windows 的瞬时错误:Windows 上用真的 ERROR_ACCESS_DENIED(走真的判定);
// 别的平台没有这种错误,换成一个测试用的错误,并让判定认它。
func transientErr(t *testing.T) error {
	if runtime.GOOS == "windows" {
		return syscall.Errno(5) // ERROR_ACCESS_DENIED
	}
	fake := errors.New("模拟:Windows 删除挂起时的拒绝访问")
	old := isEphemeral
	isEphemeral = func(err error) bool { return errors.Is(err, fake) || old(err) }
	t.Cleanup(func() { isEphemeral = old })
	return fake
}

// 文件读不出来时 Update 不存(存回去等于覆盖成默认值)。
func TestUpdateRefusesUnreadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	_ = os.WriteFile(path, []byte("{坏了"), 0o600)
	if _, err := Update(path, func(s *Settings) error { s.LogDays = 1; return nil }); err == nil {
		t.Fatal("读不出来应当报错")
	}
	if b, _ := os.ReadFile(path); string(b) != "{坏了" {
		t.Fatalf("读不出来的文件被覆盖了: %s", b)
	}
}

// 守护进程存之前,文件被别的进程改过:别人改的那几项留着,自己这次改的盖上去;没被改过就照存。
func TestSaveCheckedMergesExternal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := Default().Save(path); err != nil {
		t.Fatal(err)
	}
	base, raw, err := LoadRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Update(path, func(s *Settings) error { return s.SetWebPassword("outside-pw") }); err != nil {
		t.Fatal(err)
	}
	mine := base.Clone()
	mine.LogDays = 11
	saved, wrote, err := SaveChecked(path, base, raw, true, mine)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []Settings{saved, disk} {
		if s.LogDays != 11 || !s.CheckWebPassword("outside-pw") {
			t.Fatalf("合并不对: logDays=%d 外面改的密码还在=%v", s.LogDays, s.CheckWebPassword("outside-pw"))
		}
	}
	// 没被改过:照存,不合并(base 读不出来时同样整份覆盖)
	mine2 := saved.Clone()
	mine2.LogDays = 12
	if s, _, err := SaveChecked(path, saved, wrote, true, mine2); err != nil || s.LogDays != 12 {
		t.Fatalf("%d %v", s.LogDays, err)
	}
	over := Default()
	if s, _, err := SaveChecked(path, Default(), nil, false, over); err != nil || s.WebPassword != "" {
		t.Fatalf("base 不可信时整份覆盖: %v", err)
	}
}

// 持锁的进程没放就没了(崩溃、被杀):锁文件放久了当作已放开,不把之后的写设置一直堵着。
func TestStaleLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	lp := path + ".lock"
	if err := os.WriteFile(lp, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	_ = os.Chtimes(lp, old, old)
	if err := Default().Save(path); err != nil {
		t.Fatalf("陈旧的锁把写设置堵住了: %v", err)
	}
	if _, err := os.Stat(lp); !os.IsNotExist(err) {
		t.Fatalf("写完锁要放开: %v", err)
	}
	// 新鲜的锁:等不到就报错,不硬写
	oldWait := lockWait
	lockWait = 200 * time.Millisecond
	t.Cleanup(func() { lockWait = oldWait })
	_ = os.WriteFile(lp, nil, 0o600)
	if err := Default().Save(path); err == nil {
		t.Fatal("别人正持着锁时不该硬写")
	}
	_ = os.Remove(lp)
}
