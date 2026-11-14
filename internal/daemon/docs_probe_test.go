package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 文档里讲「定时测速测哪些节点」的几处要和 autoProbeLoop 说的一致:自动选择测 auto 组的成员(IPv6 节点、提示节点
// 不在组里,见 builder),选了线路只测这一组。以前 README 和站点文档还写着「测一轮全部节点」,和同一页里
// 「IPv6 节点、提示节点测速不测」「线路自动只测这一组」两种说法打架 —— 每处都得把这三件事说全。
func TestDocsTimedProbeScope(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(b), "\r\n", "\n")
	}
	// lineWith 含 marker 的那一行;section 从 marker 起到下一个 end 为止
	lineWith := func(rel, marker string) string {
		t.Helper()
		for _, l := range strings.Split(read(rel), "\n") {
			if strings.Contains(l, marker) {
				return l
			}
		}
		t.Fatalf("%s 里找不到 %q", rel, marker)
		return ""
	}
	section := func(rel, marker, end string) string {
		t.Helper()
		s := read(rel)
		i := strings.Index(s, marker)
		if i < 0 {
			t.Fatalf("%s 里找不到 %q", rel, marker)
		}
		s = s[i+len(marker):]
		if j := strings.Index(s, end); j >= 0 {
			s = s[:j]
		}
		return s
	}
	cases := []struct{ where, text string }{
		{"README.md 定时测速", lineWith("README.md", "- **定时测速**")},
		{"site/docs.html 选节点", section("site/docs.html", "<h3>选节点</h3>", "</p>")},
		{"wiki 常见问题", section("docs/wiki/常见问题.md", "### 「自动选择」多久换一次", "\n### ")},
		{"wiki 设置项", lineWith("docs/wiki/设置项与文件位置.md", "`probeMinutes`")},
	}
	for _, c := range cases {
		for _, want := range []string{"IPv6 节点", "提示节点", "线路"} {
			if !strings.Contains(c.text, want) {
				t.Errorf("%s 讲定时测速测哪些节点时没提「%s」:\n%s", c.where, want, c.text)
			}
		}
		if strings.Contains(c.text, "全部节点并切") || strings.Contains(c.text, "测全部节点并") {
			t.Errorf("%s 还写着定时测速测全部节点:\n%s", c.where, c.text)
		}
	}
}
