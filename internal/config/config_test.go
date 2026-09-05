package config

import "testing"

// A host provisioned under the old name keeps working: its EnvironmentFile
// says OSS_*, and the program now asks for OPSDOCTOR_*. Reading neither
// would start the service unconfigured with nothing in the log saying why.
func TestGetenvFallsBackToTheOldPrefix(t *testing.T) {
	t.Setenv("OSS_LLM_MODEL", "old")
	if got := Getenv("OPSDOCTOR_LLM_MODEL"); got != "old" {
		t.Errorf("Getenv = %q, want the OSS_ value", got)
	}
	t.Setenv("OPSDOCTOR_LLM_MODEL", "new")
	if got := Getenv("OPSDOCTOR_LLM_MODEL"); got != "new" {
		t.Errorf("Getenv = %q, want the new name to win when both are set", got)
	}
	if got := Getenv("UNRELATED"); got != "" {
		t.Errorf("Getenv(UNRELATED) = %q, want empty — the fallback is only for this program's prefix", got)
	}
}
