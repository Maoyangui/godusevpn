//go:build !windows

package paths

// secureDataDir 非 Windows 上目录本来就是 0700 建出来的(见 Ensure),没有额外要做的。
func secureDataDir(string) error { return nil }
