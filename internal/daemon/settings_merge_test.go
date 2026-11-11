package daemon

import (
	"encoding/json"
	"fmt"
	"path/filepath"
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

// setSettings 测试里整份换上一份设置。产品代码里没有这种写法:一律在此刻的设置上只改自己那几项(见 updateSettings)。
func (d *Daemon) setSettings(s settings.Settings) error {
	_, err := d.writeSettings(func(settings.Settings) (settings.Settings, bool) { return s, true })
	return err
}

// 选节点、翻「线路自动」、改设备、改订阅名、整份存设置和改面板密码同时进来:各改各的那几项,谁也不把别人刚改的盖回旧值。
// 以前这些处理器"先读整份、改几项、再整份写回",改密码落在别人读和写之间,密码就被写回旧哈希(旧网页会话随之重新有效)。
func TestSettingsHandlersConcurrentKeepEachOthersFields(t *testing.T) {
	d := lineDaemon(t)
	d.settingsGeneration = firstGeneration() // 像服务启动时那样:读设置带版本号,设置页存回来按版本合并
	selTags := []string{"hk", "jp-B", "us", "auto", "jp"}
	for round := 0; round < 30; round++ {
		pw := fmt.Sprintf("password-%d", round)
		page := getSettings(t, d) // 设置页打开时拿的那份
		page.LogDays = round + 1
		page.WebPassword = ""
		macA, macB := fmt.Sprintf("02:00:00:00:%02x:0a", round), fmt.Sprintf("02:00:00:00:%02x:0b", round)
		name := fmt.Sprintf("订阅-%d", round)
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		do := func(method string, in any) {
			raw, _ := json.Marshal(in)
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := d.Dispatch(method, raw); err != nil {
					errs <- fmt.Errorf("%s: %w", method, err)
				}
			}()
		}
		do(ipc.MSelectNode, map[string]string{"tag": selTags[round%len(selTags)]})
		do(ipc.MSetLineAuto, map[string]bool{"on": round%2 == 0})
		do(ipc.MSetDevice, map[string]string{"mac": macA, "name": "a"})
		do(ipc.MSetDevice, map[string]string{"mac": macB, "name": "b"})
		do(ipc.MRenameProfile, map[string]string{"id": "p1", "name": name})
		do(ipc.MSetSettings, page)
		do(ipc.MSetWebPassword, map[string]string{"password": pw})
		if round > 0 {
			do(ipc.MRemoveDevice, map[string]string{"mac": fmt.Sprintf("02:00:00:00:%02x:0a", round-1)})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("第 %d 轮: %v", round, err)
		}
		for _, got := range []settings.Settings{d.getSettings(), mustLoad(t)} {
			if !got.CheckWebPassword(pw) {
				t.Fatalf("第 %d 轮:同时改的面板密码被别的处理器整份写回去了", round)
			}
			macs := map[string]bool{}
			for _, dev := range got.Devices {
				macs[dev.MAC] = true
			}
			if !macs[settings.NormalizeMAC(macA)] || !macs[settings.NormalizeMAC(macB)] {
				t.Fatalf("第 %d 轮:同时加的两台设备没都留下: %+v", round, got.Devices)
			}
			if round > 0 && macs[settings.NormalizeMAC(fmt.Sprintf("02:00:00:00:%02x:0a", round-1))] {
				t.Fatalf("第 %d 轮:删掉的设备又被写回来了: %+v", round, got.Devices)
			}
			if len(got.Profiles) != 1 || got.Profiles[0].Name != name || got.LogDays != round+1 {
				t.Fatalf("第 %d 轮:订阅名 / 设置页改的那项丢了: %+v logDays=%d", round, got.Profiles, got.LogDays)
			}
		}
	}
}

func mustLoad(t *testing.T) settings.Settings {
	t.Helper()
	s, err := settings.Load(paths.Settings())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// 隐私设置收紧后重建失败、把设置退回去:只退这次改的那几项,这期间改的面板密码、设备不跟着退。
func TestRevertSettingsOnlyUndoesOwnChange(t *testing.T) {
	d := mergeTestDaemon(t)
	var prev settings.Settings
	wrote, err := d.editSettings(func(s *settings.Settings) error {
		prev = s.Clone()
		s.Mode = settings.ModeGlobal
		s.AdBlock = !s.AdBlock
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := call(t, d, ipc.MSetWebPassword, map[string]string{"password": "changed-meanwhile"}); err != nil {
		t.Fatal(err)
	}
	if err := call(t, d, ipc.MSetDevice, map[string]string{"mac": "02:00:00:00:00:01", "name": "tv"}); err != nil {
		t.Fatal(err)
	}
	if err := d.revertSettings(prev, wrote); err != nil {
		t.Fatal(err)
	}
	for _, got := range []settings.Settings{d.getSettings(), mustLoad(t)} {
		if got.Mode != prev.Mode || got.AdBlock != prev.AdBlock {
			t.Fatalf("这次改的没退回去: mode=%q adBlock=%v", got.Mode, got.AdBlock)
		}
		if !got.CheckWebPassword("changed-meanwhile") || len(got.Devices) != 1 {
			t.Fatalf("这期间别处改的跟着退了: 密码还在=%v devices=%+v", got.CheckWebPassword("changed-meanwhile"), got.Devices)
		}
	}
	// 收紧方向重建失败时走的就是它,不再整份写回 prev
	body := funcBody(t, readDaemonSource(t), "func (d *Daemon) restartForPrivacySettings(prev, wrote settings.Settings) error {")
	if !strings.Contains(body, "d.revertSettings(prev, wrote)") {
		t.Fatal("restartForPrivacySettings 的回滚应只退这次改的字段")
	}
}

// 产品代码里不再有"读整份、改、整份写回":写设置都经 updateSettings / editSettings / writeSettings 在此刻的设置上改。
func TestNoWholeSettingsWriteBack(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatal(files, err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		if src := readSource(t, f); strings.Contains(src, "d.setSettings(") {
			t.Fatalf("%s 里又出现了整份写回的 d.setSettings", f)
		}
	}
}

// 设置页在旧服务进程下打开,服务这期间重启过(自动更新、崩溃重启)、新进程里又出了几版:带回来的旧版本号不能对上新进程的
// 另一版、按错的基准合并,要认不出、让人重开设置。版本号从随机起点数,两次启动撞上的机会可以不计;也不超过 2^53(页面 JS 存得准)。
func TestSettingsRevNotReusedAcrossRestart(t *testing.T) {
	seen := map[uint64]bool{}
	for i := 0; i < 64; i++ {
		g := firstGeneration()
		if g == 0 || g >= 1<<53 || seen[g] {
			t.Fatalf("起点不对或重复: %d", g)
		}
		seen[g] = true
	}
	old := mergeTestDaemon(t)
	old.settingsGeneration = firstGeneration()
	for i := 0; i < 2; i++ {
		if err := old.updateSettings(func(s *settings.Settings) bool { s.LogDays = 20 + i; return true }); err != nil {
			t.Fatal(err)
		}
	}
	page := getSettings(t, old) // 旧进程给出去的那份(第 起点+2 版;以前两边都从 1 数,新进程里也有第 3 版)
	page.LogDays = 9

	d := mergeTestDaemon(t) // 重启后的新进程
	d.settingsGeneration = firstGeneration()
	for i := 0; i < 5; i++ { // 新进程里选节点、刷新订阅之类又出了几版
		if err := d.updateSettings(func(s *settings.Settings) bool { s.UpdateHours = 10 + i; return true }); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(page)
	if _, err := d.Dispatch(ipc.MSetSettings, raw); err == nil || !strings.Contains(err.Error(), "重新打开设置") {
		t.Fatalf("旧进程给出去的版本号应当认不出,得 %v", err)
	}
	if got := d.getSettings(); got.LogDays == 9 || got.UpdateHours != 14 {
		t.Fatalf("认不出的那份不该落下: %+v", got)
	}
	if !strings.Contains(readDaemonSource(t), "d.settingsGeneration = firstGeneration()") {
		t.Fatal("服务启动时版本号要从随机起点数")
	}
}
