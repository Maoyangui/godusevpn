package netmode

import "testing"

func TestAndroidVPNProtectionReady(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   AndroidVPNProtectionStatus
		ok   bool
		err  bool
	}{
		{"not queried", AndroidVPNProtectionStatus{}, false, true},
		{"host error", AndroidVPNProtectionStatus{Queried: true, ErrorText: "denied"}, false, true},
		{"always on only", AndroidVPNProtectionStatus{Queried: true, AlwaysOn: true}, false, false},
		{"lockdown only", AndroidVPNProtectionStatus{Queried: true, Lockdown: true}, false, false},
		{"strict protection", AndroidVPNProtectionStatus{Queried: true, AlwaysOn: true, Lockdown: true}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := androidVPNProtectionReady(tc.in)
			if got != tc.ok || (err != nil) != tc.err {
				t.Fatalf("ready(%+v)=(%v,%v), want (%v,error=%v)", tc.in, got, err, tc.ok, tc.err)
			}
		})
	}
}
