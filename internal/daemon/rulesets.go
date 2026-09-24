package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/Maoyangui/godusevpn/internal/builder"
	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/ruleset"
)

// 规则集的补齐:配置生成时本地找不到的规则集会被摘掉(不是让内核去联网现下 —— 那会让内核整个起不来,
// 见 internal/ruleset 的包注释),摘掉的记在这里,等隧道通了再去下,下次连接就齐了。
//
// 常用的三个(geosite-cn / geoip-cn / 广告)随安装包内置,正常情况下这条路一次都走不到;
// 会走到的是用户在路由规则里点了别的 geosite / geoip 类别 —— 那种规则第一次会不生效,补完才有。

// noteMissingRuleSets 记下这一轮被摘掉的规则集,并在变化时说一声。
func (d *Daemon) noteMissingRuleSets(missing []builder.MissingRuleSet) {
	d.mu.Lock()
	changed := !sameMissing(d.missingSets, missing)
	d.missingSets = missing
	d.mu.Unlock()
	if changed && len(missing) > 0 {
		tags := make([]string, 0, len(missing))
		for _, m := range missing {
			tags = append(tags, m.Tag)
		}
		d.logf("这些规则集本地还没有,用到它们的规则这一轮不生效(连上之后会自动补,下次连接生效):%s", strings.Join(tags, "、"))
	}
}

// MissingRuleSets 界面用:此刻有哪些规则集是缺的。
func (d *Daemon) MissingRuleSets() []builder.MissingRuleSet {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]builder.MissingRuleSet(nil), d.missingSets...)
}

// fillMissingRuleSets 连上之后把缺的规则集下回来。走的是守护进程自己的 HTTP 客户端 ——
// 此刻隧道已经通了,它的流量也走隧道,所以拿得到 GitHub。
// 全下完也不重启内核:那会让用户刚连上就断一下。下次连接自然就带上了。
func (d *Daemon) fillMissingRuleSets() {
	d.mu.Lock()
	if d.fillingSets {
		d.mu.Unlock()
		return
	}
	missing := append([]builder.MissingRuleSet(nil), d.missingSets...)
	if len(missing) == 0 {
		d.mu.Unlock()
		return
	}
	d.fillingSets = true
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.fillingSets = false
		d.mu.Unlock()
	}()

	// 隧道刚建好,路由和 DNS 要一会儿才稳;等一下再下,免得白白失败一轮
	time.Sleep(3 * time.Second)
	root := paths.RuleSets()
	var ok []string
	for _, m := range missing {
		if _, found := ruleset.Find(root, m.Tag); found {
			continue // 别的路径已经补上了
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		err := ruleset.Fetch(ctx, d.directHTTP(), root, m.Tag, m.URL)
		cancel()
		if err != nil {
			d.logf("规则集 %s 补下载失败(下次连接时再试): %v", m.Tag, err)
			continue
		}
		ok = append(ok, m.Tag)
	}
	if len(ok) > 0 {
		d.logf("规则集已补齐:%s —— 下次连接时生效", strings.Join(ok, "、"))
	}
}

func sameMissing(a, b []builder.MissingRuleSet) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Tag != b[i].Tag {
			return false
		}
	}
	return true
}
