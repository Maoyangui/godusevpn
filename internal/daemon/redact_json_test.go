package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

// info.json / config.redacted.json 是先序列化再整段过脱敏正则的。m29 的替换把值连同**闭合引号**一起吃掉,
// 节点名里出现 psk=xxx 这种字样时整个 JSON 就坏了 —— 诊断包里最重要的两个文件打不开。
// 这条钉住:脱敏之后仍然是合法 JSON,且明文没有留下。
func TestRedactTextKeepsJSONValid(t *testing.T) {
	in := map[string]any{
		"mode":       "rule",
		"nodes":      []string{"HK-01 psk=SuperSecret123", "token: abcdef"},
		"lastReason": "token=zzz-secret",
		"cfg":        map[string]any{"password": "p@ss", "uuid": "0f1e2d3c", "note": "authorization: Bearer xyz"},
	}
	b, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out := redactText(string(b))
	var back map[string]any
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("脱敏后不再是合法 JSON: %v\n%s", err, out)
	}
	for _, secret := range []string{"SuperSecret123", "abcdef", "zzz-secret", "p@ss", "0f1e2d3c", "xyz"} {
		if strings.Contains(out, secret) {
			t.Fatalf("明文 %q 还在:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "***") {
		t.Fatalf("没有任何脱敏痕迹:\n%s", out)
	}
}

// 引号风格要原样保留:双引号、单引号、裸值各自照旧。
func TestRedactCredentialKeepsQuoteStyle(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`"psk": "abc"`, `"psk": "***"`},
		{`psk='abc'`, `psk='***'`},
		{`psk=abc`, `psk=***`},
		{`token: abc, next`, `token: ***, next`},
	} {
		if got := redactText(c.in); got != c.want {
			t.Fatalf("redactText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
