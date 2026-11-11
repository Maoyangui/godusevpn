package settings

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
