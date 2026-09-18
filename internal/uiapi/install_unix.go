//go:build !windows

package uiapi

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Maoyangui/godusevpn/internal/svc"
	"github.com/Maoyangui/godusevpn/internal/update"
)

// installUpdate Linux 自更新:tar.gz 里取出 godusevpn 二进制,原子替换本程序,再让初始化系统重启服务。设置与订阅在数据目录,不受影响。
func installUpdate(b Backend, rel *update.Release, archive string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	tmp := exe + ".new"
	if err := extractBinary(archive, tmp); err != nil {
		return fmt.Errorf("解包: %w", err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		return fmt.Errorf("替换程序: %w", err)
	}
	_ = os.RemoveAll(filepath.Dir(archive))
	b.Logf("已更新到 v%s,重启服务", rel.Version)
	go func() {
		time.Sleep(time.Second)
		if svc.QueryStatus() == "running" {
			_ = svc.Stop()
			_ = svc.Start()
		} else {
			os.Exit(0) // 前台跑的:直接退出,由外面重新拉起
		}
	}()
	return nil
}

// extractBinary 从 tar.gz 里取出**压缩包根目录下**那个 godusevpn 写到 dst。
//
// 一定要按整条路径认,不能按文件名认:macOS 的发布包里有两个文件都叫 godusevpn ——
// 根目录下的守护进程,和 godusevpn.app/Contents/MacOS/godusevpn 那个图形界面。
// 早先是"basename 对上就用、第一个命中就返回",tar 里谁在前面就抽谁;抽到界面那一份的话,
// /usr/local/bin/godusevpn 会被换成一个 Cocoa 窗口程序,launchd 以 root 反复拉起它,
// VPN 彻底不能用,而且文档里教的 `godusevpn uninstall` / `status` 也一起没了(那个二进制不认这些子命令),
// 用户只能重装。
func extractBinary(archive, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg || !isRootBinary(h.Name) {
			continue
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, tr)
		out.Close()
		return err
	}
	return errors.New("压缩包里没有根目录下的 godusevpn")
}
