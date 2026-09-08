package uiapi

import (
	"errors"

	"github.com/Maoyangui/godusevpn/internal/update"
)

// Windows 的自更新在托盘客户端里做(安装包);这套共用实现不在 Windows 上用。
func installUpdate(Backend, *update.Release, string) error {
	return errors.New("Windows 上请用客户端升级")
}
