package config

import "testing"

// A host provisioned under the old name keeps working: its EnvironmentFile
// says OPSPILOT_*, OPSDOCTOR_* or OSS_*, and the program now asks for STEWARD_*. Reading neither
// would start the service unconfigured with nothing in the log saying why.
func TestGetenvFallsBackToTheOldPrefix(t *testing.T) {
	t.Setenv("OSS_LLM_MODEL", "oldest")
	if got := Getenv("STEWARD_LLM_MODEL"); got != "oldest" {
		t.Errorf("Getenv = %q, want the OSS_ value", got)
	}
	t.Setenv("OPSDOCTOR_LLM_MODEL", "old")
	if got := Getenv("STEWARD_LLM_MODEL"); got != "old" {
		t.Errorf("Getenv = %q, want the OPSDOCTOR_ value over the OSS_ one", got)
	}
	t.Setenv("OPSPILOT_LLM_MODEL", "newer")
	if got := Getenv("STEWARD_LLM_MODEL"); got != "newer" {
		t.Errorf("Getenv = %q, want the OPSPILOT_ value over the OPSDOCTOR_ one", got)
	}
	t.Setenv("STEWARD_LLM_MODEL", "new")
	if got := Getenv("STEWARD_LLM_MODEL"); got != "new" {
		t.Errorf("Getenv = %q, want the new name to win when both are set", got)
	}
	if got := Getenv("UNRELATED"); got != "" {
		t.Errorf("Getenv(UNRELATED) = %q, want empty — the fallback is only for this program's prefix", got)
	}
}

func TestAlchemyIsOffUntilAnAddressIsGiven(t *testing.T) {
	if got := Load(); got.AlchemyAddr != "" || got.AlchemyTLS {
		t.Errorf("alchemy configured from nothing: %+v", got)
	}
	t.Setenv("STEWARD_ALCHEMY_ADDR", "127.0.0.1:43711")
	t.Setenv("STEWARD_ALCHEMY_TLS", "Yes")
	got := Load()
	if got.AlchemyAddr != "127.0.0.1:43711" || !got.AlchemyTLS {
		t.Errorf("alchemy = %q tls=%v", got.AlchemyAddr, got.AlchemyTLS)
	}
}
