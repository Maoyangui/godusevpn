package daemon

import (
	"testing"
	"time"
)

// 连接期间新冒出公网 v6 的网卡要尽快停掉(地址露出来几十秒就够被读走),但真关不掉时又不能每几秒白跑一次
// PowerShell。nicRetryDue 管的就是"这一轮去不去跑停用脚本"。
func TestNICRetryDue(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		quiet bool
		last  time.Time
		want  bool
	}{
		{"没退避:每轮都跑", false, now.Add(-time.Second), true},
		{"没退避、从没跑过", false, time.Time{}, true},
		{"退避中、刚跑过:不跑", true, now.Add(-time.Minute), false},
		{"退避中、差一秒满 10 分钟:不跑", true, now.Add(-nicQuietRetry + time.Second), false},
		{"退避中、正好满 10 分钟:跑", true, now.Add(-nicQuietRetry), true},
		{"退避中、早就满了:跑", true, now.Add(-time.Hour), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nicRetryDue(c.quiet, c.last, now); got != c.want {
				t.Fatalf("得到 %v,应为 %v", got, c.want)
			}
		})
	}
}

// 巡检间隔是这条修复的本体:原来 30 秒才看一次,新网卡拿到公网 v6 之后最多半分钟才停。
// 看一次只是标准库枚举网卡地址(很便宜),间隔放宽之前先想清楚暴露窗口。
func TestNICWatchIsFrequent(t *testing.T) {
	if nicWatchEvery > 5*time.Second {
		t.Fatalf("网卡巡检间隔 %v 太长:新网卡上的公网 IPv6 会在这段时间里露在外面", nicWatchEvery)
	}
	if nicQuietRetry < time.Minute {
		t.Fatalf("退避后的重试间隔 %v 太短:关不掉的网卡会让停用脚本频繁运行", nicQuietRetry)
	}
}
