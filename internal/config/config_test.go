package config

import (
	"testing"

	"github.com/spf13/viper"
)

func TestAuditEnabled_DefaultTrueWhenUnset(t *testing.T) {
	viper.Reset()
	if !AuditEnabled() {
		t.Error("AuditEnabled() should default to true when audit.enabled is unset")
	}
}

func TestAuditEnabled_RespectsExplicitValue(t *testing.T) {
	viper.Reset()
	defer viper.Reset()

	viper.Set("audit.enabled", false)
	if AuditEnabled() {
		t.Error("AuditEnabled() should be false when audit.enabled=false")
	}

	viper.Set("audit.enabled", true)
	if !AuditEnabled() {
		t.Error("AuditEnabled() should be true when audit.enabled=true")
	}
}
