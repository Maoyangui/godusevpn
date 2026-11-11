package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/paths"
)

// 几处同时写连接状态(连接 / 断开拿着 connOpMu,删掉最后一个订阅、托盘「恢复网络」那几条路以前不拿):
// 以前都写同一个 state.json.tmp 再改名,一个刚改完名、另一个还在往那个文件写或者紧跟着改名,就会报"文件不存在",
// 或者把别人写了一半的内容改名成 state.json。现在每次写自己的临时文件:谁也不报错,落下来的总是完整的一份,目录里不留临时文件。
func TestSavePersistedConcurrent(t *testing.T) {
	t.Setenv("GODUSEVPN_DATA", t.TempDir())
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	d := newTestDaemon(t)
	var wg sync.WaitGroup
	errs := make(chan error, 16*100)
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if err := d.savePersisted(persisted{Wanted: (g+i)%2 == 0}); err != nil {
					errs <- err
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	n := 0
	for err := range errs {
		if n < 3 {
			t.Errorf("并发写连接状态出错: %v", err)
		}
		n++
	}
	if n > 0 {
		t.Fatalf("共 %d 次写失败", n)
	}
	b, err := os.ReadFile(paths.State())
	if err != nil {
		t.Fatal(err)
	}
	var p persisted
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("落下来的连接状态不完整: %q: %v", b, err)
	}
	left, _ := filepath.Glob(filepath.Join(paths.DataDir(), "*"))
	for _, f := range left {
		if strings.HasSuffix(f, ".tmp") || strings.Contains(filepath.Base(f), ".tmp") {
			t.Fatalf("临时文件没清掉: %v", left)
		}
	}
	if !d.persistedStateOK() {
		t.Fatal("写成功后应标记连接状态可信")
	}
}
