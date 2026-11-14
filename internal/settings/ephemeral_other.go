//go:build !windows

package settings

// ephemeralErr 别的平台上删文件、改名替换都是原子的,也没有强制的共享模式:不存在"等一下就好"的文件错误,
// 拒绝访问就是真的没权限,不重试。
func ephemeralErr(error) bool { return false }
