//go:build !windows

package web

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Maoyangui/godusevpn/internal/paths"
	"github.com/Maoyangui/godusevpn/internal/svc"
	"github.com/Maoyangui/godusevpn/internal/update"
)

// applyUpdate Linux 自更新:下载 tar.gz(内含 godusevpn 二进制),校验后原子替换本程序,再让初始化系统重启服务。
// 设置与订阅都在数据目录,不受影响。
func (s *Server) applyUpdate(ctx context.Context) error {
	s.mu.Lock()
	rel := s.update
	if s.updating || rel == nil {
		s.mu.Unlock()
		return errors.New("没有可用的更新")
	}
	s.updating = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.updating = false; s.mu.Unlock() }()

	dir := filepath.Join(paths.DataDir(), "update")
	_ = os.RemoveAll(dir)
	archive, err := update.Download(ctx, rel, dir, nil, func(done, total int64) {
		s.broadcast("update-progress", map[string]int64{"done": done, "total": total})
	})
	if err != nil {
		return err
	}
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
	_ = os.RemoveAll(dir)
	s.b.Logf("已更新到 v%s,重启服务", rel.Version)
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

// extractBinary 从 tar.gz 里取出名为 godusevpn 的文件写到 dst。
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
		if h.Typeflag != tar.TypeReg || filepath.Base(h.Name) != "godusevpn" {
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
	return errors.New("压缩包里没有 godusevpn")
}
