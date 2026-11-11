package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/ipc"
	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/settings"
)

// mergeTestDaemon 设置文件按 NewWithOptions 那样读进来(记着读到的原始字节),之后写设置要和别的进程合并。
func mergeTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := profileOpsDaemon(t)
	if err := settings.Default().Save(paths.Settings()); err != nil {
		t.Fatal(err)
	}
	s, raw, err := settings.LoadRaw(paths.Settings())
	if err != nil {
		t.Fatal(err)
	}
	d.settings, d.settingsGeneration = s, 1
	d.diskBase, d.diskRaw, d.diskOK = s, raw, true
	return d
}

func getSettings(t *testing.T, d *Daemon) settings.Settings {
	t.Helper()
	res, err := d.Dispatch(ipc.MGetSettings, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.(settings.Settings)
}

// 两边拿着同一版设置各改一项、同时存:两项都留下,谁也不把对方那项盖回旧值(界面的设置页和命令行 settings 同时保存)。
// 改密码另走 SetWebPassword,同时整份保存设置不把它盖掉。
func TestSetSettingsConcurrentKeepsBoth(t *testing.T) {
	d := mergeTestDaemon(t)
	for round := 0; round < 20; round++ {
		base := getSettings(t, d)
		if base.Rev == 0 {
			t.Fatal("读设置要带上版本号")
		}
		a, b := base, base
		a.LogDays = 10 + round
		b.UpdateHours = 30 + round
		b.BypassApps = []string{fmt.Sprintf("app-%d.exe", round)}
		pw := fmt.Sprintf("password-%d", round)
		var wg sync.WaitGroup
		errs := make(chan error, 3)
		for _, s := range []settings.Settings{a, b} {
			raw, _ := json.Marshal(s)
			wg.Add(1)
			go func() { defer wg.Done(); _, err := d.Dispatch(ipc.MSetSettings, raw); errs <- err }()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, _ := json.Marshal(map[string]string{"password": pw})
			_, err := d.Dispatch(ipc.MSetWebPassword, raw)
			errs <- err
		}()
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		got := d.getSettings()
		if got.LogDays != 10+round || got.UpdateHours != 30+round || strings.Join(got.BypassApps, ",") != fmt.Sprintf("app-%d.exe", round) {
			t.Fatalf("第 %d 轮:同时保存的两项没都留下: logDays=%d updateHours=%d bypass=%v", round, got.LogDays, got.UpdateHours, got.BypassApps)
		}
		if !got.CheckWebPassword(pw) {
			t.Fatalf("第 %d 轮:同时改的密码被整份保存盖掉了", round)
		}
		disk, err := settings.Load(paths.Settings())
		if err != nil || disk.LogDays != got.LogDays || disk.UpdateHours != got.UpdateHours || !disk.CheckWebPassword(pw) {
			t.Fatalf("第 %d 轮:落盘的和内存里的对不上: %+v %v", round, disk, err)
		}
	}
}

// 拿着旧版整份存回来时,只有它改了的字段生效:这期间别处改的选择、密码不被退回去。版本太旧(早就记不得)时报错让人重开,不瞎合并。
func TestSetSettingsMergesOnlyChangedFields(t *testing.T) {
	d := mergeTestDaemon(t)
	old := getSettings(t, d)
	if err := d.updateSettings(func(s *settings.Settings) bool {
		s.AdBlock = !s.AdBlock
		_ = s.SetWebPassword("changed-elsewhere")
		return true
	}); err != nil {
		t.Fatal(err)
	}
	mine := old
	mine.LogDays = 42
	mine.WebPassword = "" // 页面拿到的那份不带密码哈希
	raw, _ := json.Marshal(mine)
	res, err := d.Dispatch(ipc.MSetSettings, raw)
	if err != nil {
		t.Fatal(err)
	}
	got := d.getSettings()
	if got.LogDays != 42 || got.AdBlock == old.AdBlock || !got.CheckWebPassword("changed-elsewhere") {
		t.Fatalf("合并不对: logDays=%d adBlock=%v(原来 %v) 密码还在=%v", got.LogDays, got.AdBlock, old.AdBlock, got.CheckWebPassword("changed-elsewhere"))
	}
	if out := res.(settings.Settings); out.Rev == 0 || out.LogDays != 42 {
		t.Fatalf("存完返回的设置要带新版本号: %+v", out.Rev)
	}
	stale := old
	stale.Rev = 1 << 40
	raw, _ = json.Marshal(stale)
	if _, err := d.Dispatch(ipc.MSetSettings, raw); err == nil {
		t.Fatal("认不出的版本号应当报错,而不是整份覆盖")
	}
}

// 守护进程存设置之前,文件被别的进程改过(服务刚起、控制口还没开时命令行 passwd 直接改了文件;「恢复网络」的离线兜底):
// 别人改的那几项留着,守护进程这次改的盖上去,内存里的也跟上。
func TestWriteSettingsMergesExternalFileChange(t *testing.T) {
	d := mergeTestDaemon(t)
	var wg sync.WaitGroup
	const n = 20
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if _, err := settings.Update(paths.Settings(), func(s *settings.Settings) error {
				s.BypassApps = append(s.BypassApps, fmt.Sprintf("ext-%d.exe", i))
				return nil
			}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			if err := d.updateSettings(func(s *settings.Settings) bool { s.LogDays = i + 1; return true }); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()
	// 外面最后一次写在守护进程最后一次写之后时,内存里还没看到;再存一次就合并进来
	if err := d.updateSettings(func(s *settings.Settings) bool { s.UpdateHours = 7; return true }); err != nil {
		t.Fatal(err)
	}
	disk, err := settings.Load(paths.Settings())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []settings.Settings{disk, d.getSettings()} {
		if len(s.BypassApps) != n || s.LogDays != n || s.UpdateHours != 7 {
			t.Fatalf("两边并发写设置有丢的: bypass=%d 条 logDays=%d updateHours=%d", len(s.BypassApps), s.LogDays, s.UpdateHours)
		}
	}
}
