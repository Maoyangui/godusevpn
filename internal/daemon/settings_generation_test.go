package daemon

import (
	"crypto/sha256"
	"testing"

	"github.com/Maoyangui/godusevpn/internal/settings"
)

func TestPreparedSettingsRejectsChangedPrivacyPolicy(t *testing.T) {
	s := settings.Default()
	cfg := []byte(`{"route":{"final":"proxy"}}`)
	d := &Daemon{
		settings:                   s,
		settingsGeneration:         7,
		preparedSettingsGeneration: 7,
		preparedSettingsValid:      true,
		preparedConfigHash:         sha256.Sum256(cfg),
	}
	if err := d.preparedMatchesCurrent(cfg); err != nil {
		t.Fatalf("unchanged settings rejected: %v", err)
	}
	if err := d.preparedMatchesCurrent([]byte(`{"route":{"final":"direct"}}`)); err == nil {
		t.Fatal("metadata for another parallel prepare accepted")
	}

	next := s.Clone()
	next.Mode = settings.ModeGlobal
	next.NoDirect = true
	d.settings = next
	d.settingsGeneration++
	if err := d.preparedMatchesCurrent(cfg); err == nil {
		t.Fatal("stale config accepted after privacy settings changed")
	}
}
