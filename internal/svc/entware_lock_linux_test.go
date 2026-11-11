package svc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// runWithin 测试进程自己跑一遍脚本(同 run),最多等 limit:超时就杀掉,返回 timedOut。
func (r *entwareRig) runWithin(limit time.Duration, args ...string) (out string, err error, took time.Duration, timedOut bool) {
	sh := "sh"
	if r.busybox {
		sh = filepath.Join(r.bin, "sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	c := exec.CommandContext(ctx, sh, append([]string{r.script}, args...)...)
	c.Env = r.env
	began := time.Now()
	b, err := c.CombinedOutput()
	return string(b), err, time.Since(began), ctx.Err() != nil
}

// deadPID 一个已经退出(收过尸)的进程号。
func deadPID(t *testing.T) int {
	t.Helper()
	c := exec.Command("true")
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	return c.Process.Pid
}

// 梅林(Entware):拉起那一段的锁判定为旧锁之后要删掉它,可锁目录所在的地方删不动(/opt 被重新挂成只读:断电留下旧锁,
// U 盘文件系统出错下次挂成只读是常见的事)时,以前是删一下、不等不数接着再来,永远出不了循环:开机的 rc.unslung start、
// godusevpn stop / uninstall 都卡死,CPU 吃满,每一圈还往标准错误刷一行 rm 的报错。现在清不掉就和锁目录建不成一样,
// 试几回之后不加锁照以前那样起。几种旧锁都试:持有者不在了、记录没写上(等满时限)、清旧锁用的清理锁也是被打断留下的。
// 锁所在的目录改成只读(测试不是 root,权限挡得住)。
func TestEntwareLockUnremovable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 不受目录权限限制,造不出删不掉的锁")
	}
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gvlkro", busybox)
		runDir := filepath.Dir(r.lockDir())
		t.Cleanup(func() { _ = os.Chmod(runDir, 0o755) })
		for _, c := range []struct {
			what   string
			owner  string // 空 = 记录没写上
			clean  bool   // 清理锁也留着
			ttl3   bool   // 把时限改成 3 秒(记录没写上的要等满时限)
			within time.Duration
		}{
			{what: "持有者不在了", owner: fmt.Sprintf("%d %d", deadPID(t), uptimeSeconds(t)), within: 10 * time.Second},
			{what: "记录没写上", ttl3: true, within: 15 * time.Second},
			{what: "清理锁也留着", owner: fmt.Sprintf("%d %d", deadPID(t), uptimeSeconds(t)), clean: true, within: 20 * time.Second},
		} {
			_ = os.Chmod(runDir, 0o755)
			_ = os.RemoveAll(runDir)
			if err := os.MkdirAll(r.lockDir(), 0o755); err != nil {
				t.Fatal(err)
			}
			if c.owner != "" {
				r.write("run/godusevpn.lock/owner", c.owner+"\n", 0o644)
			}
			if c.clean {
				if err := os.Mkdir(r.lockDir()+".clean", 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if c.ttl3 {
				r.write("S99godusevpn", strings.Replace(r.localize(entwareScript), "LOCKTTL=30", "LOCKTTL=3", 1), 0o755)
			} else {
				r.render(true)
			}
			if err := os.Chmod(runDir, 0o555); err != nil {
				t.Fatal(err)
			}
			out, err, took, timedOut := r.runWithin(40*time.Second, startAction(initEntware))
			if timedOut {
				t.Fatalf("%s:旧锁删不掉时一直出不了循环(%v 还没完)\n%.2000s", c.what, took, out)
			}
			if err != nil || took > c.within {
				t.Fatalf("%s:应在 %v 内不加锁照样起来,用了 %v: %v\n%s", c.what, c.within, took, err, out)
			}
			if ds := entwareDaemonsByScan(r.name); len(ds) != 1 {
				t.Fatalf("%s:应起了一个守护进程,得 %v\n%s", c.what, ds, out)
			}
			if strings.Contains(out, "rm:") || strings.Contains(out, "rmdir:") {
				t.Fatalf("%s:删不掉的报错不该刷到输出里:\n%s", c.what, out)
			}
			// stop 同样不会卡住
			out, err, took, timedOut = r.runWithin(40*time.Second, "stop")
			if timedOut || err != nil {
				t.Fatalf("%s:stop 应照样做完(用了 %v,超时=%v): %v\n%s", c.what, took, timedOut, err, out)
			}
			waitFor(t, c.what+":守护进程停掉", func() bool { return len(entwareDaemonsByScan(r.name)) == 0 })
		}
		_ = os.Chmod(runDir, 0o755)
	})
}

// lockHarness 只跑脚本里拉起那一段的锁(entwareLockSh):等 go 文件出现(几个一起起跑)→ 拿锁 → 在锁里建 inside 目录
// (建不成说明别人也在锁里,打一行 OVERLAP)→ 待一会儿 → 删掉 → 放锁。
func lockHarness(t *testing.T, r *entwareRig) string {
	t.Helper()
	body := strings.Replace(entwareLockSh, "LOCK=/opt/var/run/godusevpn.lock\n", "LOCK="+r.lockDir()+"\n", 1)
	if body == entwareLockSh {
		t.Fatalf("锁那一段变了,测试里的替换要跟上:\n%s", entwareLockSh)
	}
	d := r.dir
	r.write("harness", "#!/bin/sh\n"+body+`
while [ ! -e "`+d+`/go" ]; do :; done
lock
mkdir "`+d+`/inside" 2>/dev/null || echo OVERLAP
sleep 0.2 2>/dev/null || sleep 1
rmdir "`+d+`/inside" 2>/dev/null
unlock
echo DONE
`, 0o755)
	return filepath.Join(d, "harness")
}

// 梅林(Entware):几个脚本同时等同一把旧锁(持有者被 KILL 或断电留下的)时,以前清旧锁是"看记录对不对"和"删"两步:
// 先删的紧接着建好了自己的锁,后删的还拿着判定时的结论,把这把新锁也删了,两边同时进了锁里 —— 正是这把锁要防的
// 两边各拉起一个守护进程、stop 和看门狗的拉起交错。现在清旧锁要先拿清理锁,拿着它再确认还是判定的那一把才删。
// 每一轮放一把持有者已经不在的旧锁,8 个一起起跑抢锁,谁都不能和别人同时在锁里,也都要拿到锁做完;
// 最后清理锁被打断留下(清的那个被 KILL)时也不会一直等下去。
func TestEntwareLockStaleRace(t *testing.T) {
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gvlkrc", busybox)
		harness := lockHarness(t, r)
		sh := "sh"
		if busybox {
			sh = filepath.Join(r.bin, "sh")
		}
		stale := func() {
			t.Helper()
			_ = os.RemoveAll(r.lockDir())
			if err := os.MkdirAll(r.lockDir(), 0o755); err != nil {
				t.Fatal(err)
			}
			r.write("run/godusevpn.lock/owner", fmt.Sprintf("%d %d\n", deadPID(t), uptimeSeconds(t)), 0o644)
		}
		round := func(what string, n int, limit time.Duration) {
			t.Helper()
			_ = os.Remove(filepath.Join(r.dir, "go"))
			outs := make([]string, n)
			var wg sync.WaitGroup
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			for i := 0; i < n; i++ {
				c := exec.CommandContext(ctx, sh, harness)
				c.Env = r.env
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					b, err := c.CombinedOutput()
					outs[i] = fmt.Sprintf("%s(%v)", b, err)
				}(i)
			}
			time.Sleep(300 * time.Millisecond) // 都起来、都在等 go
			r.write("go", "", 0o644)
			wg.Wait()
			if ctx.Err() != nil {
				t.Fatalf("%s:%v 内没都拿到锁做完:\n%s", what, limit, strings.Join(outs, "\n"))
			}
			all := strings.Join(outs, "\n")
			if strings.Contains(all, "OVERLAP") {
				t.Fatalf("%s:有两个同时进了锁里:\n%s", what, all)
			}
			if strings.Count(all, "DONE") != n {
				t.Fatalf("%s:不是每个都做完了:\n%s", what, all)
			}
			if r.locked() {
				t.Fatalf("%s:做完要放锁", what)
			}
			if _, err := os.Stat(r.lockDir() + ".clean"); err == nil {
				t.Fatalf("%s:清理锁要放掉", what)
			}
		}
		for i := 0; i < 6; i++ {
			stale()
			round(fmt.Sprintf("第 %d 轮", i+1), 8, 60*time.Second)
		}
		// 清理锁被打断留下:等一会儿(清理只要一眨眼)当它是留下的,删掉接着清旧锁
		stale()
		if err := os.Mkdir(r.lockDir()+".clean", 0o755); err != nil {
			t.Fatal(err)
		}
		began := time.Now()
		round("清理锁被打断留下", 2, 30*time.Second)
		if d := time.Since(began); d < 3*time.Second {
			t.Fatalf("清理锁可能有人正拿着,要等一会儿才当它是留下的(只等了 %v)", d)
		}
	})
}
