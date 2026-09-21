//go:build windows

package netmode

import "testing"

func TestValidNICBackupLines(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines []string
		want  bool
	}{
		{"valid", []string{"Wi-Fi\tTrue", "Ethernet\tFalse"}, true},
		{"empty name", []string{"\tTrue"}, false},
		{"bad state", []string{"Wi-Fi\t1"}, false},
		{"extra field", []string{"Wi-Fi\tTrue\textra"}, false},
		{"duplicate", []string{"Wi-Fi\tTrue", "Wi-Fi\tFalse"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validNICBackupLines(tc.lines); got != tc.want {
				t.Fatalf("validNICBackupLines(%q) = %v, want %v", tc.lines, got, tc.want)
			}
		})
	}
}
