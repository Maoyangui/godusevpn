package state

import "testing"

// 隐私核查类错误(闸、网卡 IPv6)在 watch 里**不能**触发拆隧道:闸还在时拆了只是白断网,
// 闸缺失或规则模式下拆了等于全部直连。这条钉住判定本身;拆不拆由 watch 按它决定。
func TestPrivacyCodesNeverRebuild(t *testing.T) {
	for _, code := range []string{CodePrivacyGuard, CodePrivacyNIC} {
		if !isPrivacyCode(code) {
			t.Fatalf("%s 应被识别为隐私核查错误", code)
		}
	}
	for _, code := range []string{CodeCoreCrash, CodeNodeDown, ""} {
		if isPrivacyCode(code) {
			t.Fatalf("%q 不是隐私核查错误", code)
		}
	}
}
