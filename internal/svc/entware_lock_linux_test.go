package svc

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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
			if strings.Contains(out, "rm:") || strings.Contains(out, "rmdir:") || strings.Contains(out, "mv:") {
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

// lockHarness 只跑脚本里拉起那一段的锁(entwareLockSh):等 go 文件出现(几个一起起跑)→ 拿锁 → 在锁里独占地建 inside
// (建不成说明别人也在锁里,打一行 OVERLAP)→ 待一会儿 → 删掉 → 放锁。prelude 接在锁那一段后面(换掉里面用的命令之类)。
// inside 用 set -C 的重定向建(shell 自己 O_EXCL 打开),不用 mkdir:uutils 0.8.0 的 mkdir 两个同时建会都返回 0
// (见 uutilsMkdir),用它测重叠,恰好同时进锁的两个同时建 inside 时就看不出来;prelude 换掉的 mkdir 也碰不到它。
func lockHarness(t *testing.T, r *entwareRig, prelude string) string {
	t.Helper()
	body := strings.Replace(entwareLockSh, "LOCK=/opt/var/run/godusevpn.lock\n", "LOCK="+r.lockDir()+"\n", 1)
	if body == entwareLockSh {
		t.Fatalf("锁那一段变了,测试里的替换要跟上:\n%s", entwareLockSh)
	}
	d := r.dir
	r.write("harness", "#!/bin/sh\n"+body+prelude+`
while [ ! -e "`+d+`/go" ]; do :; done
lock
[ -n "$MYLOCK" ] || echo NOLOCK
(set -C; : > "`+d+`/inside") 2>/dev/null || echo OVERLAP
sleep 0.2 2>/dev/null || sleep 1
rm -f "`+d+`/inside"
unlock
echo DONE
`, 0o755)
	return filepath.Join(d, "harness")
}

// staleLock 放一把持有者已经不在了的旧锁(持有者被 KILL 或断电留下的那种)。
func staleLock(t *testing.T, r *entwareRig) {
	t.Helper()
	_ = os.RemoveAll(r.lockDir())
	if err := os.MkdirAll(r.lockDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	r.write("run/godusevpn.lock/owner", fmt.Sprintf("%d %d\n", deadPID(t), uptimeSeconds(t)), 0o644)
}

// lockRound n 个 harness(见 lockHarness)一起起跑抢锁:limit 内都要拿到锁做完,谁都不能和别人同时在锁里、
// 不能没拿到锁就进去,做完锁和清理锁都要放掉。
func lockRound(t *testing.T, r *entwareRig, harness, what string, n int, limit time.Duration) {
	t.Helper()
	sh := "sh"
	if r.busybox {
		sh = filepath.Join(r.bin, "sh")
	}
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
	if strings.Contains(all, "NOLOCK") {
		t.Fatalf("%s:锁目录建得了,却有没拿到锁就进去的:\n%s", what, all)
	}
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

// 梅林(Entware):几个脚本同时等同一把旧锁(持有者被 KILL 或断电留下的)时,以前清旧锁是"看记录对不对"和"删"两步:
// 先删的紧接着建好了自己的锁,后删的还拿着判定时的结论,把这把新锁也删了,两边同时进了锁里 —— 正是这把锁要防的
// 两边各拉起一个守护进程、stop 和看门狗的拉起交错。现在清旧锁要先拿清理锁,拿着它再确认还是判定的那一把才删。
// 每一轮放一把持有者已经不在的旧锁,8 个一起起跑抢锁,谁都不能和别人同时在锁里,也都要拿到锁做完;
// 最后清理锁被打断留下(清的那个被 KILL)时也不会一直等下去。
func TestEntwareLockStaleRace(t *testing.T) {
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gvlkrc", busybox)
		harness := lockHarness(t, r, "")
		for i := 0; i < 6; i++ {
			staleLock(t, r)
			lockRound(t, r, harness, fmt.Sprintf("第 %d 轮", i+1), 8, 60*time.Second)
		}
		// 清理锁被打断留下:等一会儿(清理只要一眨眼)当它是留下的,删掉接着清旧锁
		staleLock(t, r)
		if err := os.Mkdir(r.lockDir()+".clean", 0o755); err != nil {
			t.Fatal(err)
		}
		began := time.Now()
		lockRound(t, r, harness, "清理锁被打断留下", 2, 30*time.Second)
		if d := time.Since(began); d < 3*time.Second {
			t.Fatalf("清理锁可能有人正拿着,要等一会儿才当它是留下的(只等了 %v)", d)
		}
	})
}

// uutilsMkdir 照 uutils coreutils 0.8.0(Ubuntu 26.04 默认的 /usr/bin/mkdir)不带 -p 时的做法换掉脚本里的 mkdir:
// 先看在不在,在就报错;不在再建,建的时候内核报 EEXIST(别人恰好在这中间建了)只要那里是个目录就当建成、返回 0
// (上游 issue 13970,0.8.0 之后才修)。"看"和"建"中间停一小会儿,把真机上几微秒的空当拉宽,好稳定地撞上。
const uutilsMkdir = `
mkdir() {
	case "$1" in -*) command mkdir "$@"; return ;; esac
	[ -e "$1" ] && return 1
	sleep 0.02 2>/dev/null
	command mkdir "$1" 2>/dev/null || [ -d "$1" ]
}
`

// 梅林(Entware)的拉起锁是 mkdir 建锁目录:以前只看 mkdir 的返回值,建成了就算拿到。可 GitHub 的 ubuntu-latest
// (Ubuntu 26.04)上 /usr/bin/mkdir 是 uutils coreutils 0.8.0,几个一起建同一个目录时会有两个都返回 0(见 uutilsMkdir),
// TestEntwareLockStaleRace 在那里第 1 轮就报两个同时进了锁里(测试机是 Ubuntu 24.04 的 GNU mkdir,从来撞不上)。
// 锁的对错不能押在外部 mkdir 的返回值上:现在 mkdir 之后还要在目录里用 set -C 独占地建成认领文件(锁目录是 owner,
// 清理锁是 by)才算拿到。这里把脚本里的 mkdir 换成 uutils 那样的,8 个一起起跑:先抢一把持有者不在了的旧锁
// (清理锁、锁目录都要抢),再从没有锁开始抢,谁都不能和别人同时在锁里。
func TestEntwareLockMkdirLies(t *testing.T) {
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gvlkml", busybox)
		harness := lockHarness(t, r, uutilsMkdir)
		for i := 0; i < 4; i++ {
			staleLock(t, r)
			lockRound(t, r, harness, fmt.Sprintf("旧锁第 %d 轮", i+1), 8, 60*time.Second)
		}
		for i := 0; i < 2; i++ {
			_ = os.RemoveAll(r.lockDir())
			lockRound(t, r, harness, fmt.Sprintf("没有锁第 %d 轮", i+1), 8, 60*time.Second)
		}
	})
}

// 梅林(Entware):放锁、清旧锁要删锁目录时,等锁的正往里留记号(.w进程号;记号刚被删、或者记录刚被删读成换了一把,
// 都会再留)。以前直接 rm -rf:一项项删完最后才 rmdir,记号这时又写进去,rmdir 因为目录不空失败,剩下一把没有记录、
// 只有记号的锁 —— 等锁的都要等满 LOCKTTL(30 秒)才清得掉,清旧锁时还算一回"清不掉",数满三回就不加锁照样起;
// 放锁那一下还往标准错误打一行 rm 的报错。现在先把锁目录整个改名挪开再删。
// 测试进程开几个 goroutine 不停往锁目录里建记号(比真的等锁的勤得多),放锁、清旧锁都要把锁目录删干净、不报错、不耽搁。
func TestEntwareLockDropUnderWaiterMarks(t *testing.T) {
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gvlkdm", busybox)
		body := strings.Replace(entwareLockSh, "LOCK=/opt/var/run/godusevpn.lock\n", "LOCK="+r.lockDir()+"\n", 1)
		if body == entwareLockSh {
			t.Fatalf("锁那一段变了,测试里的替换要跟上:\n%s", entwareLockSh)
		}
		d := r.dir
		// 拿锁 → 等 release 出现 → 放锁
		r.write("holder", "#!/bin/sh\n"+body+`
lock
[ -n "$MYLOCK" ] || echo NOLOCK
while [ ! -e "`+d+`/release" ]; do :; done
unlock
echo DONE
`, 0o755)
		sh := "sh"
		if busybox {
			sh = filepath.Join(r.bin, "sh")
		}
		mark := filepath.Join(r.lockDir(), ".w99999")
		hammer := func() (stop func()) {
			done := make(chan struct{})
			var wg sync.WaitGroup
			for i := 0; i < 3; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						select {
						case <-done:
							return
						default:
						}
						if f, err := os.OpenFile(mark, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
							_ = f.Close()
						}
					}
				}()
			}
			return func() { close(done); wg.Wait() }
		}
		run := func(what string, limit time.Duration) {
			t.Helper()
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			c := exec.CommandContext(ctx, sh, filepath.Join(d, "holder"))
			c.Env = r.env
			b, err := c.CombinedOutput()
			out := string(b)
			if ctx.Err() != nil {
				t.Fatalf("%s:%v 内没拿到锁做完:\n%s", what, limit, out)
			}
			if err != nil || strings.Count(out, "DONE") != 1 || strings.Contains(out, "NOLOCK") {
				t.Fatalf("%s:应拿到锁做完: %v\n%s", what, err, out)
			}
			if strings.Contains(out, "rm:") || strings.Contains(out, "rmdir:") || strings.Contains(out, "mv:") {
				t.Fatalf("%s:不该有删除的报错:\n%s", what, out)
			}
		}
		reset := func() {
			t.Helper()
			_ = os.RemoveAll(r.lockDir())
			r.write("release", "", 0o644) // 拿到锁马上放
		}
		// 放锁:持有时记号不停地写进来
		for i := 0; i < 10; i++ {
			reset()
			stop := hammer()
			run(fmt.Sprintf("放锁 第 %d 回", i+1), 20*time.Second)
			stop()
			if r.locked() {
				ents, _ := os.ReadDir(r.lockDir())
				t.Fatalf("放锁 第 %d 回:锁目录没删掉,剩下 %v", i+1, ents)
			}
		}
		// 清旧锁:持有者不在了的旧锁里记号不停地写进来,要马上清掉拿到锁(不是等满 LOCKTTL,也不是不加锁照样起)
		for i := 0; i < 5; i++ {
			reset()
			if err := os.MkdirAll(r.lockDir(), 0o755); err != nil {
				t.Fatal(err)
			}
			r.write("run/godusevpn.lock/owner", fmt.Sprintf("%d %d\n", deadPID(t), uptimeSeconds(t)), 0o644)
			stop := hammer()
			began := time.Now()
			run(fmt.Sprintf("清旧锁 第 %d 回", i+1), 40*time.Second)
			stop()
			if took := time.Since(began); took > 10*time.Second {
				t.Fatalf("清旧锁 第 %d 回:用了 %v,应马上清掉", i+1, took)
			}
			if r.locked() {
				ents, _ := os.ReadDir(r.lockDir())
				t.Fatalf("清旧锁 第 %d 回:锁目录没删掉,剩下 %v", i+1, ents)
			}
		}
		// 挪开后没删完的(挪开前找到目录、挪开后才写进去的那一下)不挡锁,之后放锁时一并清掉
		reset()
		if err := os.MkdirAll(r.lockDir()+".dead.1", 0o755); err != nil {
			t.Fatal(err)
		}
		r.write("run/godusevpn.lock.dead.1/.w2", "", 0o644)
		run("留着挪开的", 10*time.Second)
		if left, _ := filepath.Glob(r.lockDir() + "*"); len(left) != 0 {
			t.Fatalf("放锁后不该剩下锁目录或挪开的:%v", left)
		}
	})
}

// 梅林(Entware):拿锁、清旧锁时 mkdir 建不成、再一看又不在 —— 等锁的一多,这多半是别人刚好在这中间建了又放掉(抢先了),
// 不是这里建不了。以前一律算一回"建不成 / 清不掉",一次等锁里数满三回就不加锁进去,和拿着锁的同时在锁里
// (GitHub 的 ubuntu runner 上 TestEntwareLockStaleRace 第 1 轮就这么出过三个 OVERLAP)。脚本里的 mkdir 换成一个
// 函数,前几回对锁目录 / 清理锁假装"被别人抢先了"(不建、返回失败,目录照样不在),确定地造出这个交错:目录建得了时
// 要接着等、拿到锁,不能不加锁进去。名字上是个文件或 FIFO(怎么都建不成)时照旧试几回就不加锁照样起,不卡住、不空转。
func TestEntwareLockLostRaceNotCounted(t *testing.T) {
	eachToolset(t, func(t *testing.T, busybox bool) {
		r := newEntwareRig(t, "gvlklr", busybox)
		body := strings.Replace(entwareLockSh, "LOCK=/opt/var/run/godusevpn.lock\n", "LOCK="+r.lockDir()+"\n", 1)
		if body == entwareLockSh {
			t.Fatalf("锁那一段变了,测试里的替换要跟上:\n%s", entwareLockSh)
		}
		// LOST 是要假装抢输的那个目录,前 5 回建它都"被别人抢先"
		r.write("lost", "#!/bin/sh\n"+body+`
lost=0
mkdir() {
	if [ "$1" = "$LOST" ] && [ $lost -lt 5 ]; then
		lost=$((lost + 1))
		return 1
	fi
	command mkdir "$@"
}
lock
[ -n "$MYLOCK" ] || echo NOLOCK
echo "LOST=$lost"
unlock
echo DONE
`, 0o755)
		sh := "sh"
		if busybox {
			sh = filepath.Join(r.bin, "sh")
		}
		run := func(lost string, limit time.Duration) string {
			t.Helper()
			ctx, cancel := context.WithTimeout(context.Background(), limit)
			defer cancel()
			c := exec.CommandContext(ctx, sh, filepath.Join(r.dir, "lost"))
			c.Env = append(append([]string{}, r.env...), "LOST="+lost)
			b, err := c.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("%s:%v 内没做完(卡住了):\n%s", lost, limit, b)
			}
			if err != nil || !strings.Contains(string(b), "DONE") {
				t.Fatalf("%s:应做完: %v\n%s", lost, err, b)
			}
			return string(b)
		}
		// 锁目录不在、建它前 5 回都被抢先:每回都是马上再建,不用等
		_ = os.RemoveAll(r.lockDir())
		_ = os.MkdirAll(filepath.Dir(r.lockDir()), 0o755)
		if out := run(r.lockDir(), 10*time.Second); strings.Contains(out, "NOLOCK") || !strings.Contains(out, "LOST=5") {
			t.Fatalf("锁目录被抢先几回不能算建不成、不加锁进去:\n%s", out)
		}
		if r.locked() {
			t.Fatal("锁目录被抢先:做完要放锁")
		}
		// 有一把持有者不在了的旧锁,清它要拿的清理锁前 5 回都被抢先:每回隔一秒再看,最后清掉、拿到锁
		if err := os.MkdirAll(r.lockDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		r.write("run/godusevpn.lock/owner", fmt.Sprintf("%d %d\n", deadPID(t), uptimeSeconds(t)), 0o644)
		if out := run(r.lockDir()+".clean", 20*time.Second); strings.Contains(out, "NOLOCK") || !strings.Contains(out, "LOST=5") {
			t.Fatalf("清理锁被抢先几回不能算清不掉、不加锁进去:\n%s", out)
		}
		if left, _ := filepath.Glob(r.lockDir() + "*"); len(left) != 0 {
			t.Fatalf("清理锁被抢先:做完不该剩下锁目录、清理锁或挪开的:%v", left)
		}
		// 名字上是个文件:怎么都建不成,试几回就不加锁照样起,不卡住、不空转
		r.write("run/godusevpn.lock", "", 0o644)
		if out := run("-", 10*time.Second); !strings.Contains(out, "NOLOCK") {
			t.Fatalf("锁目录的名字上是个文件,应不加锁照样起:\n%s", out)
		}
		_ = os.Remove(r.lockDir())
		// 名字上是个 FIFO(socket、设备文件同理):不是普通文件也一样怎么都建不成,要照文件那样试几回就不加锁照样起。
		// 以前只认普通文件和链接,FIFO 被当成"被抢先"马上再建,锁目录上一直空转(不 sleep),清理锁上一直等
		if err := syscall.Mkfifo(r.lockDir(), 0o644); err != nil {
			t.Fatal(err)
		}
		if out := run("-", 10*time.Second); !strings.Contains(out, "NOLOCK") {
			t.Fatalf("锁目录的名字上是个 FIFO,应不加锁照样起:\n%s", out)
		}
		_ = os.Remove(r.lockDir())
		// 有一把持有者不在了的旧锁,清理锁的名字上是个 FIFO:清不掉,试三回(每回隔一秒)就不加锁照样起
		if err := os.MkdirAll(r.lockDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		r.write("run/godusevpn.lock/owner", fmt.Sprintf("%d %d\n", deadPID(t), uptimeSeconds(t)), 0o644)
		if err := syscall.Mkfifo(r.lockDir()+".clean", 0o644); err != nil {
			t.Fatal(err)
		}
		if out := run("-", 15*time.Second); !strings.Contains(out, "NOLOCK") {
			t.Fatalf("清理锁的名字上是个 FIFO,应试几回就不加锁照样起:\n%s", out)
		}
		if left, _ := filepath.Glob(r.lockDir() + ".dead.*"); len(left) != 0 {
			t.Fatalf("试建用的目录不该留下:%v", left)
		}
		_ = os.Remove(r.lockDir() + ".clean")
		_ = os.RemoveAll(r.lockDir())
	})
}
