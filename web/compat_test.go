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
