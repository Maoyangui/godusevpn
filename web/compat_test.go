package web

import (
	"io/fs"
	"strings"
	"testing"
)

// 页面这一套是四端共用的,其中最老的运行环境是 Android 的系统 WebView。
// 中国版的电视盒子 / 智能电视(小米电视 EA70 这类)不走 Google 认证,系统自带的 WebView 常年停在 Chrome 66 左右,
// 而且永远不会更新。JS 的新语法是**解析期**就报错的:文件里任何一处写了内核不认的写法,整份脚本一行都不会执行,
// 页面就是一片白 —— 2026-09-17 用户的小米电视 EA70 上出的就是这个:app.js 第 4 行一个 `??`,整个界面打不开,
// 只剩 index.html 的静态骨架。
//
// 所以这里定一条底线:dist 下的 JS 只能用 Chrome 66 认识的写法。要用更新的语法,得先给页面加上打包 / 转译
// 这一步,而不是直接往 dist 里写。判断依据可以用 acorn 复核:acorn.parse(src, {ecmaVersion: 2018}) 能过就行。
const minChrome = 66

// 解析期就会让整份脚本失败的写法(最危险:整页白屏,且没有任何提示)。
var bannedSyntax = []struct {
	pat, name string
	chrome    int
}{
	{"??", "空值合并 ??", 80},
	{"||=", "逻辑或赋值 ||=", 85},
	{"&&=", "逻辑与赋值 &&=", 85},
}

// 解析没问题、但执行到那一行才抛异常的新 API(只坏掉一个功能,不会整页白)。
var bannedAPI = []struct {
	pat, name string
	chrome    int
}{
	{".replaceAll(", "String.replaceAll", 85},
	{".flatMap(", "Array.flatMap", 69},
	{".flat(", "Array.flat", 69},
	{"Object.fromEntries", "Object.fromEntries", 73},
	{"Object.hasOwn", "Object.hasOwn", 93},
	{"Promise.allSettled", "Promise.allSettled", 76},
	{"Promise.any", "Promise.any", 85},
	{"globalThis", "globalThis", 71},
	{"structuredClone", "structuredClone", 98},
	{".matchAll(", "String.matchAll", 73},
	{"queueMicrotask", "queueMicrotask", 71},
}

// TestDistJSRunsOnOldWebView 扫一遍 dist 下的 JS,挡住电视那种老 WebView 认不了的写法。
//
// 只把注释抹掉(注释里讲这些写法是正常的,比如上面这段),字符串照扫:
// 宁可偶尔误报一次、由人来确认,也不要因为剥离器本身有 bug 而漏过真正的问题 —— 漏报的代价是用户白屏。
// 真遇到字符串里正当出现这些字符的情况,把那处改个写法,或者在这里加白名单。
func TestDistJSRunsOnOldWebView(t *testing.T) {
	files, err := fs.Glob(dist, "dist/*.js")
	if err != nil || len(files) == 0 {
		t.Fatalf("没找到 dist 下的 js: %v", err)
	}
	for _, f := range files {
		b, err := fs.ReadFile(dist, f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		code := stripComments(string(b))
		for _, r := range bannedSyntax {
			for _, ln := range findAll(code, r.pat) {
				t.Errorf("%s 第 %d 行用了 %s(要 Chrome %d,底线是 %d)—— 解析期错误,整份脚本都不执行,页面会白屏",
					f, ln, r.name, r.chrome, minChrome)
			}
		}
		// 可选链 ?. 单独判:三元运算写成 `a ?.5 : b` 也长这样,所以点后面跟数字的不算
		for i := 0; i+2 < len(code); i++ {
			if code[i] == '?' && code[i+1] == '.' && (code[i+2] < '0' || code[i+2] > '9') {
				t.Errorf("%s 第 %d 行用了可选链 ?.(要 Chrome 80,底线是 %d)—— 解析期错误,整页会白",
					f, lineAt(code, i), minChrome)
			}
		}
		for _, r := range bannedAPI {
			if r.chrome <= minChrome {
				continue
			}
			for _, ln := range findAll(code, r.pat) {
				t.Errorf("%s 第 %d 行用了 %s(要 Chrome %d,底线是 %d)—— 执行到这一行就抛异常",
					f, ln, r.name, r.chrome, minChrome)
			}
		}
	}
}

// TestBootWatchdogStaysES5 白屏自曝那段脚本是最后一道防线:页面里别的脚本被内核拒了,全靠它把原因显示出来。
// 它自己要是也用了老内核不认的写法,就跟着一起哑掉了,用户还是只看到一片白。
func TestBootWatchdogStaysES5(t *testing.T) {
	b, err := fs.ReadFile(dist, "dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	i := strings.Index(html, "<script>")
	j := strings.Index(html, "</script>")
	if i < 0 || j < i {
		t.Fatal("index.html 里找不到内联的白屏自曝脚本")
	}
	boot := html[i:j]
	if !strings.Contains(boot, "boot-error") {
		t.Fatal("第一段内联脚本不是白屏自曝那段,顺序被人动过了")
	}
	for _, bad := range []struct{ pat, name string }{
		{"=>", "箭头函数"}, {"const ", "const"}, {"let ", "let"}, {"`", "模板字符串"},
		{"??", "空值合并"}, {"?.", "可选链"}, {"class ", "class"},
	} {
		if strings.Contains(boot, bad.pat) {
			t.Errorf("白屏自曝脚本里用了 %s —— 这段必须是 ES5,否则在出问题的那种老内核上它自己也跑不起来", bad.name)
		}
	}
}

func lineAt(s string, i int) int { return strings.Count(s[:i], "\n") + 1 }

// findAll 返回 pat 出现的所有行号。
func findAll(s, pat string) []int {
	var out []int
	for off := 0; ; {
		i := strings.Index(s[off:], pat)
		if i < 0 {
			return out
		}
		out = append(out, lineAt(s, off+i))
		off += i + len(pat)
	}
}

// stripComments 把 // 与 /* */ 注释换成空格(换行保留,行号才对得上)。
// 只处理注释,不碰字符串:注释里出现这些写法是正常的(讲解用),字符串里出现则基本可以肯定是真的写错了。
// 唯一要小心的是正则字面量里的 / ,所以这里要求 // 前面是行首或空白 —— 正则里的 // 不会长这样。
func stripComments(s string) string {
	out := []byte(s)
	blank := func(i int) {
		if out[i] != '\n' {
			out[i] = ' '
		}
	}
	for i := 0; i+1 < len(out); i++ {
		if out[i] == '/' && out[i+1] == '/' && (i == 0 || out[i-1] == ' ' || out[i-1] == '\t' || out[i-1] == '\n' || out[i-1] == '\r') {
			for i < len(out) && out[i] != '\n' {
				blank(i)
				i++
			}
			continue
		}
		if out[i] == '/' && out[i+1] == '*' {
			for i+1 < len(out) && !(out[i] == '*' && out[i+1] == '/') {
				blank(i)
				i++
			}
			if i+1 < len(out) {
				blank(i)
				blank(i + 1)
				i++
			}
		}
	}
	return string(out)
}

// ---- CSS 侧 ----
//
// 这一侧原来一条测试都没有,而电视上出过的两次问题都在 CSS 里:flex 的 gap 在 Chrome 66 上不生效,
// 一行里的元素全贴在一起;抽屉那一格没写 overflow/min-height,菜单最后一项掉到屏幕外还滚不到。
// 老内核遇到不认识的属性是**静默丢弃那一条声明** —— 不报错、不白屏,只是布局悄悄错掉,
// 而我们在新机器上永远看不到。所以只能靠这里挡。

// bannedCSS 里 fallback 不为空的,表示"可以用,但同一条规则里得先有一条老内核认识的写法垫底"。
var bannedCSS = []struct {
	pat, name, fallback string
	chrome              int
}{
	{pat: "overflow: clip", name: "overflow: clip", chrome: 90, fallback: "overflow: hidden"},
	{pat: "inset:", name: "inset 简写", chrome: 87},
	{pat: ":is(", name: ":is()", chrome: 88},
	{pat: ":where(", name: ":where()", chrome: 88},
	{pat: "aspect-ratio:", name: "aspect-ratio", chrome: 88},
	{pat: "clamp(", name: "clamp()", chrome: 79},
	{pat: "min(", name: "min()", chrome: 79, fallback: ";"}, // 前面得有一条同属性的定值声明
	{pat: "max(", name: "max()", chrome: 79, fallback: ";"},
	{pat: "backdrop-filter:", name: "backdrop-filter", chrome: 76, fallback: "background"}, // 退化成不模糊,能接受
	{pat: "content-visibility:", name: "content-visibility", chrome: 85},
	{pat: "accent-color:", name: "accent-color", chrome: 93},
	{pat: "position: sticky", name: "position: sticky", chrome: 56}, // 66 上有,列在这里只是备忘
}

func TestDistCSSRunsOnOldWebView(t *testing.T) {
	for _, f := range cssFiles(t) {
		css := stripComments(readDist(t, f))
		for _, r := range bannedCSS {
			if r.chrome <= minChrome {
				continue
			}
			for _, at := range offsets(css, r.pat) {
				if r.fallback != "" && ruleAt(css, at, r.pat) != "" && declBefore(css, at, r.pat, r.fallback) {
					continue
				}
				t.Errorf("%s 第 %d 行用了 %s(要 Chrome %d,底线是 %d)—— 老内核会静默丢掉这条声明,页面不报错但布局是错的%s",
					f, lineAt(css, at), r.name, r.chrome, minChrome,
					fallbackHint(r.fallback))
			}
		}
	}
}

func fallbackHint(fb string) string {
	if fb == "" {
		return ""
	}
	if fb == ";" {
		return ";要用的话同一条规则里先写一条定值垫底"
	}
	return ";要用的话同一条规则里先写一条老写法(" + fb + ")垫底"
}

// TestFlexGapHasNogapFallback flex 的 gap 要到 Chrome 84 才有。页面靠 index.html 里那段脚本实测一次,
// 不管用就给 <html> 加 nogap 类,再由 .nogap 那批相邻兄弟选择器用 margin 补回间距。
// 漏掉任何一个用了 gap 的 flex 容器,那一行在电视上间距就是 0 —— 图标、名字、延迟全糊在一起。
func TestFlexGapHasNogapFallback(t *testing.T) {
	css := stripComments(readDist(t, "dist/style.css"))
	sels := flexGapSelectors(css)
	// 解析器要是哪天被 CSS 的写法变化弄瞎了,这条测试会变成"一个都没找到、于是全过",
	// 那比没有测试更糟。样式表里 flex + gap 的容器有五十多个,给个下限钉住。
	if len(sels) < 40 {
		t.Fatalf("只解析出 %d 个 flex+gap 选择器,解析器八成瞎了:%v", len(sels), sels)
	}
	for _, sel := range sels {
		// 兜底规则长这样:.nogap <选择器> > *:not([hidden]) + *:not([hidden]) { margin-left: … }
		// 也允许写成 .nogap body.xxx <选择器>(平台特化那几条)
		if !strings.Contains(css, ".nogap "+sel+" > *") && !strings.Contains(css, ".nogap "+strings.TrimPrefix(sel, ".")+" > *") &&
			!regexpContainsNogap(css, sel) {
			t.Errorf("选择器 %s 是 flex 且用了 gap,却没有对应的 .nogap 兜底规则 —— 电视那种老内核上这一行间距会变成 0", sel)
		}
	}
}

func regexpContainsNogap(css, sel string) bool {
	// 平台特化写法:.nogap body.android .drawer-brand > *…
	for _, ln := range strings.Split(css, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), ".nogap ") && strings.Contains(ln, sel+" > *") {
			return true
		}
	}
	return false
}

// flexGapSelectors 找出"同一条规则里既 display: flex 又写了 gap"的选择器。
func flexGapSelectors(css string) []string {
	var out []string
	seen := map[string]bool{}
	for _, block := range strings.Split(css, "}") {
		i := strings.Index(block, "{")
		if i < 0 {
			continue
		}
		sel, body := strings.TrimSpace(block[:i]), block[i+1:]
		if sel == "" || strings.HasPrefix(sel, "@") || strings.Contains(sel, "@media") {
			// 媒体查询里 selector 会带上前缀,取最后一段
			if j := strings.LastIndex(sel, "{"); j >= 0 {
				sel = strings.TrimSpace(sel[j+1:])
			}
		}
		if sel == "" || strings.HasPrefix(sel, "@") {
			continue
		}
		if !strings.Contains(body, "gap:") || strings.Contains(body, "grid-gap") {
			continue
		}
		if !strings.Contains(body, "display: flex") && !strings.Contains(body, "display: inline-flex") {
			continue // grid 的 gap 在 Chrome 66 上有(grid-gap),不归这条管
		}
		sel = strings.TrimSpace(sel[strings.LastIndex(sel, "\n")+1:])
		if sel != "" && !seen[sel] {
			seen[sel] = true
			out = append(out, sel)
		}
	}
	return out
}

// TestDrawerListScrolls 抽屉那一格必须能滚。电视的 WebView 视口常常只有 960×540,菜单一屏装不下;
// flex 子项不写 min-height: 0 会被"最小内容尺寸"撑开,内容溢出到屏幕外面,而且没有任何可滚的容器 ——
// 遥控器把焦点移过去了,画面上什么也没动。2026-09-18 用户的索尼电视上「关于」就是这么消失的,
// 连带着导出诊断包、检查更新、退出全都够不着。两条缺一不可。
func TestDrawerListScrolls(t *testing.T) {
	css := stripComments(readDist(t, "dist/style.css"))
	i := strings.Index(css, ".drawer-items {")
	if i < 0 {
		t.Fatal("找不到 .drawer-items 规则 —— 选择器被改名了,这条测试要跟着更新")
	}
	rule := css[i:]
	if j := strings.Index(rule, "}"); j > 0 {
		rule = rule[:j]
	}
	for _, need := range []string{"overflow-y: auto", "min-height: 0"} {
		if !strings.Contains(rule, need) {
			t.Errorf(".drawer-items 少了 %q —— 少任意一条,电视上菜单最后几项就会掉到屏幕外面且滚不到。规则现在是:%s", need, rule)
		}
	}
}

// ---- 上面几条要用的小工具 ----

func cssFiles(t *testing.T) []string {
	t.Helper()
	files, err := fs.Glob(dist, "dist/*.css")
	if err != nil || len(files) == 0 {
		t.Fatalf("没找到 dist 下的 css: %v", err)
	}
	return files
}

func readDist(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(dist, name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return string(b)
}

// offsets pat 出现的所有字节位置。
func offsets(s, pat string) []int {
	var out []int
	for off := 0; ; {
		i := strings.Index(s[off:], pat)
		if i < 0 {
			return out
		}
		out = append(out, off+i)
		off += i + len(pat)
	}
}

// ruleAt 取 at 所在的那条规则的内容({ 与 } 之间)。
func ruleAt(s string, at int, _ string) string {
	start := strings.LastIndex(s[:at], "{")
	if start < 0 {
		return ""
	}
	end := strings.Index(s[at:], "}")
	if end < 0 {
		return s[start+1:]
	}
	return s[start+1 : at+end]
}

// declBefore 同一条规则里,这个位置**之前**有没有垫底的老写法。
// fallback 为 ";" 表示"同属性先有一条定值声明"(比如 height: 800px; height: min(…)):
// 只要这条规则里、匹配点之前还有别的声明就算数。
func declBefore(s string, at int, pat, fallback string) bool {
	start := strings.LastIndex(s[:at], "{")
	if start < 0 {
		return false
	}
	before := s[start+1 : at]
	if fallback == ";" {
		// 取当前属性名,看它前面是不是已经出现过一次
		lineStart := strings.LastIndexAny(before, ";{") + 1
		prop := strings.TrimSpace(before[lineStart:])
		if i := strings.Index(prop, ":"); i > 0 {
			prop = strings.TrimSpace(prop[:i])
		}
		return prop != "" && strings.Contains(before[:lineStart], prop+":")
	}
	return strings.Contains(before, fallback)
}
