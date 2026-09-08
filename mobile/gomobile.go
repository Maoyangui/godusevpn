//go:build linux || android

package mobile

// gomobile bind 要求本模块直接依赖 golang.org/x/mobile,否则报 missing dependency;这里空引用一下让 go mod tidy 留住它。
import _ "golang.org/x/mobile/bind"
