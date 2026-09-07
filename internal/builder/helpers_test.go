package builder

import "os"

func writeFile(p string, b []byte) error { return os.WriteFile(p, b, 0o600) }
