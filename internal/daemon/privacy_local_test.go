package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/logx"
	"github.com/Maoyangui/godusevpn/internal/paths"
)

func coreLogDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := &Daemon{coreLog: logx.New(filepath.Join(t.TempDir(), "core.log"), 1<<20, 2)}
	t.Cleanup(func() { _ = d.coreLog.Close() })
	return d
}

func diskText(t *testing.T, d *Daemon) string {
	t.Helper()
	_ = d.coreLog.Close()
	b, err := os.ReadFile(d.coreLog.Path())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// 默认:连接目标不落盘,日志页(内存)照样看得到;失败的连接也带域名,落盘前打码。
func TestCoreLogDestinationsStayInMemory(t *testing.T) {
	d := coreLogDaemon(t)
	d.corePrintf("INFO", "%s", "outbound/direct[direct]: outbound connection to www.visited-site.com:443")
	d.corePrintf("ERROR", "%s", "inbound/tun: open connection to www.failed-site.com:443: i/o timeout")

	disk := diskText(t, d)
	for _, s := range []string{"visited-site", "failed-site", "outbound connection"} {
		if strings.Contains(disk, s) {
			t.Errorf("磁盘上不该有 %s:\n%s", s, disk)
		}
	}
	if !strings.Contains(disk, "ERROR") || !strings.Contains(disk, "i/o timeout") {
		t.Errorf("错误和原因要留着:\n%s", disk)
	}
	mem := strings.Join(d.coreTail(10), "\n")
	for _, s := range []string{"www.visited-site.com", "www.failed-site.com"} {
		if !strings.Contains(mem, s) {
			t.Errorf("日志页应看得到 %s:\n%s", s, mem)
		}
	}
}

// 打开「写入磁盘」:和老版本一样全写;内存日志里订阅令牌照样打码(和落盘的同一道)。
func TestCoreLogDiskOn(t *testing.T) {
	d := coreLogDaemon(t)
	d.initCoreLog(true)
	d.corePrintf("INFO", "%s", "outbound connection to www.visited-site.com:443")
	d.corePrintf("INFO", "%s", `Get "https://panel.example:2056/sub/SECRETTOKEN?format=json"`)
	disk := diskText(t, d)
	if !strings.Contains(disk, "www.visited-site.com") {
		t.Fatalf("开着时应写全:\n%s", disk)
	}
	for where, s := range map[string]string{"磁盘": disk, "内存": strings.Join(d.coreTail(5), "\n")} {
		if strings.Contains(s, "SECRETTOKEN") {
			t.Errorf("%s里有订阅令牌:\n%s", where, s)
		}
	}
	// 内存里不够 n 行(服务刚重启)时读文件
	if got := d.coreTail(100); len(got) != 2 || !strings.Contains(got[0], "visited-site") {
		t.Fatalf("实际 %v", got)
	}
}

// 老版本写下的完整日志:升级后第一次启动按新规矩过一遍;这一行打完码再过一遍不再变,不会每次启动都重写文件。
func TestCoreLogOldFileFiltered(t *testing.T) {
	d := coreLogDaemon(t)
	d.settingsOK = true
	old := "2026-10-01 10:00:00 INFO outbound connection to www.visited-site.com:443\n" +
		"2026-10-01 10:00:01 ERROR open connection to www.failed-site.com:443 via 142.250.72.4: refused\n" +
		"2026-10-01 10:00:02 WARN found neighbor: aa:bb:cc:dd:ee:ff, hostname: kids-ipad\n" +
		"not a log line www.other-site.com\n"
	if err := os.WriteFile(d.coreLog.Path(), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d.coreLog.Path()+".1", []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	d.initCoreLog(false)
	for _, p := range []string{d.coreLog.Path(), d.coreLog.Path() + ".1"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, bad := range []string{"visited-site", "failed-site", "other-site", "142.250.72.4", "aa:bb:cc", "kids-ipad"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s 里还有 %s:\n%s", p, bad, s)
			}
		}
		if !strings.Contains(s, "refused") {
			t.Errorf("%s 把错误原因也删了:\n%s", p, s)
		}
		for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
			if again, ok := coreDiskLine(l); !ok || again != l {
				t.Errorf("打码后再过一遍变了: %q → %q", l, again)
			}
		}
	}
	if got := strings.Join(d.coreTail(10), "\n"); !strings.Contains(got, "refused") {
		t.Fatalf("重启后日志页应能看到留下来的错误: %s", got)
	}
}

// 设置读不出来时不动日志:那时的"默认关"不是用户的意思。
func TestCoreLogUntouchedWhenSettingsBad(t *testing.T) {
	d := coreLogDaemon(t)
	old := "2026-10-01 10:00:00 INFO outbound connection to www.visited-site.com:443\n"
	if err := os.WriteFile(d.coreLog.Path(), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	d.initCoreLog(false)
	if b, _ := os.ReadFile(d.coreLog.Path()); string(b) != old {
		t.Fatalf("不该动: %s", b)
	}
}

func writeCache(t *testing.T, age time.Duration) string {
	t.Helper()
	p := builder.CachePath(paths.DataDir())
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("fakeip"), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(p) })
	return p
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// 缓存里的 fake-ip 对照表就是访问过的域名:刚断开不删(很快又连上时要对得上),闲置够久才删;
// 服务启动时上次不是连着关的直接删,连着关的同样看闲置。
func TestPurgeCoreCache(t *testing.T) {
	d := &Daemon{}
	if p := writeCache(t, time.Minute); true {
		d.purgeCoreCache(false)
		if !exists(p) {
			t.Fatal("刚断开不该删")
		}
		d.purgeCoreCache(true)
		if exists(p) {
			t.Fatal("上次不是连着关的,启动时应直接删")
		}
	}
	if p := writeCache(t, coreCacheIdle+time.Minute); true {
		d.purgeCoreCache(false)
		if exists(p) {
			t.Fatal("闲置够久应删")
		}
	}
	d.purgeCoreCache(true) // 文件不存在也无妨
}
