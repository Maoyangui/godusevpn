package web

import (
	"context"
	"errors"
)

// Windows 的自更新在托盘客户端里做(安装包),内置面板不在 Windows 上用。
func (s *Server) applyUpdate(context.Context) error {
	return errors.New("Windows 上请用客户端升级")
}
