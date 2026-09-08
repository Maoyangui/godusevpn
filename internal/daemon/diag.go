package daemon

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/Maoyangui/godusevpn/internal/buildinfo"
	"github.com/Maoyangui/godusevpn/internal/logx"
	"github.com/Maoyangui/godusevpn/internal/paths"
)

// 诊断包:日志、脱敏后的配置与设置、路由表、网卡信息。凭据类字段一律打码,订阅地址只留主机名。
var secretKeys = map[string]bool{"password": true, "uuid": true, "private_key": true, "psk": true, "pre_shared_key": true, "secret": true, "token": true, "auth": true, "auth_str": true, "obfs_password": true, "up_password": true, "down_password": true}

func redact(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if secretKeys[strings.ToLower(k)] {
				x[k] = "***"
			} else {
				x[k] = redact(val)
			}
		}
		return x
	case []any:
		for i := range x {
			x[i] = redact(x[i])
		}
		return x
	default:
		return v
	}
}

var subPath = regexp.MustCompile(`(/sub/)[^/?#]+`)

func redactURL(u string) string { return subPath.ReplaceAllString(u, "${1}***") }

func cmdOut(name string, args ...string) string {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("(%s %s: %v)\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// exportDiag 生成 zip,返回路径。
func (d *Daemon) exportDiag() (string, error) {
	dir := filepath.Join(paths.DataDir(), "diag")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "godusevpn-diag-"+time.Now().Format("20060102-150405")+".zip")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	add := func(name, content string) {
		w, err := zw.Create(name)
		if err == nil {
			_, _ = w.Write([]byte(content))
		}
	}
	view := d.stateView()
	view.Settings.ProfileURL = redactURL(view.Settings.ProfileURL)
	if view.Profile != nil {
		view.Profile.URL = redactURL(view.Profile.URL)
	}
	for i := range view.Profiles {
		view.Profiles[i].URL = redactURL(view.Profiles[i].URL)
	}
	for i := range view.Settings.Profiles {
		view.Settings.Profiles[i].URL = redactURL(view.Settings.Profiles[i].URL)
	}
	info := map[string]any{"version": buildinfo.Version, "os": runtime.GOOS + "/" + runtime.GOARCH, "time": time.Now().Format(time.RFC3339), "state": view}
	if _, p := d.activeProfile(); p != nil {
		info["servers"] = p.Servers()
	}
	b, _ := json.MarshalIndent(info, "", "  ")
	add("info.json", string(b))
	if raw, err := os.ReadFile(paths.Config()); err == nil {
		var cfg any
		if json.Unmarshal(raw, &cfg) == nil {
			rb, _ := json.MarshalIndent(redact(cfg), "", "  ")
			add("config.redacted.json", string(rb))
		}
	}
	add("service.log", strings.Join(logx.Tail(d.log.Path(), 500), "\n"))
	add("core.log", strings.Join(logx.Tail(d.coreLog.Path(), 500), "\n"))
	sysDiag(add)
	if err := zw.Close(); err != nil {
		return "", err
	}
	return path, nil
}
