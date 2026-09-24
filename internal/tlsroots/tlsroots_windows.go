//go:build windows

package tlsroots

import (
	"context"
	"crypto/x509"
	"fmt"
	"unsafe"

	"github.com/sagernet/sing-box/common/certificate"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/logger"
	"golang.org/x/sys/windows"
)

var (
	status  = "没有设置(照旧走 Windows 平台校验)"
	mozilla int
	local   int
)

func init() {
	// 必须在任何证书校验之前:包初始化早于 main,也早于 sing-box 建证书库。
	store, err := certificate.NewStore(context.Background(), logger.NOP(), option.CertificateOptions{Store: C.CertificateStoreMozilla})
	if err != nil {
		status = "内置根证书读不出来(照旧走 Windows 平台校验): " + err.Error()
		return
	}
	pool := store.Pool()
	if pool == nil {
		status = "内置根证书为空(照旧走 Windows 平台校验)"
		return
	}
	mozilla = len(pool.Subjects()) //nolint:staticcheck // 普通证书池,Subjects 照常可用
	local = addLocalRoots(pool)
	x509.SetFallbackRoots(pool)
	status = fmt.Sprintf("内置 Mozilla 根证书 %d 张 + 本机受信任根证书 %d 张,Go 自己校验(不走 Windows 平台校验)", mozilla, local)
}

// addLocalRoots 把本机"受信任的根证书颁发机构"(以服务身份跑时是计算机的)里的证书加进 pool,返回加了几张
// (含与内置重复的)。枚举失败就少加几张,内置的那份照样在。
func addLocalRoots(pool *x509.CertPool) int {
	name, err := windows.UTF16PtrFromString("ROOT")
	if err != nil {
		return 0
	}
	store, err := windows.CertOpenSystemStore(0, name)
	if err != nil {
		return 0
	}
	defer windows.CertCloseStore(store, 0)
	n := 0
	var prev *windows.CertContext
	for {
		// 传入上一张会把它释放掉;返回空(连同错误)就是枚举完了,那时上一张也已经释放
		cur, err := windows.CertEnumCertificatesInStore(store, prev)
		if err != nil || cur == nil {
			return n
		}
		der := make([]byte, cur.Length)
		copy(der, unsafe.Slice(cur.EncodedCert, cur.Length))
		if c, err := x509.ParseCertificate(der); err == nil {
			pool.AddCert(c)
			n++
		}
		prev = cur
	}
}

// Status 设置情况,给服务启动日志和诊断用。
func Status() string {
	if !fallbackForced() {
		return status + ";但主程序没打开 x509usefallbackroots,仍走 Windows 平台校验"
	}
	return status
}

// fallbackForced 主程序有没有打开 x509usefallbackroots:打开了,SystemCertPool 拿到的就是上面那份(普通证书池,
// 有证书);没打开,Windows 上拿到的是"交给平台校验"的空标记池。
func fallbackForced() bool {
	p, err := x509.SystemCertPool()
	return err == nil && p != nil && len(p.Subjects()) > 0 //nolint:staticcheck
}

// Active 真的生效了:内置根证书装上了,主程序也打开了开关。
func Active() bool { return mozilla > 0 && fallbackForced() }
