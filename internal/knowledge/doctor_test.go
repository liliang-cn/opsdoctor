package knowledge

import (
	"strings"
	"testing"
)

// The drift report existed and was computed on every inventory, but nothing
// printed it — so the answer to "did the extractor invent types" was reachable
// only by writing Go against the library. Undeclared EDGE types are the ones
// worth interrupting for: expansion filters on the declared vocabulary, so they
// are stored and never walked.
func TestDoctorReportsInventedTypes(t *testing.T) {
	got := checkVocabularyDrift(&Inventory{Drift: &OntologyDrift{
		Registered:          true,
		Fingerprint:         "abc",
		StoredFingerprint:   "abc",
		UndeclaredEdgeTypes: map[string]int{"DEPLOYED_ON": 12, "MANAGES": 3},
		UndeclaredNodeTypes: map[string]int{"StorageClass": 3},
	}}, nil)

	if got.Status != CheckWarn {
		t.Errorf("status = %q, want warn", got.Status)
	}
	if !strings.Contains(got.Detail, "DEPLOYED_ON") {
		t.Errorf("detail = %q, want it to name the worst offender", got.Detail)
	}
}

// A graph extracted under one vocabulary and queried under another has every
// edge in place and expands to nothing. It outranks invented types because the
// symptom is identical to having no graph at all.
func TestDoctorReportsAGraphExtractedUnderAnotherVocabulary(t *testing.T) {
	got := checkVocabularyDrift(&Inventory{Drift: &OntologyDrift{
		Registered: true, Fingerprint: "new", StoredFingerprint: "old",
	}}, nil)

	if got.Status != CheckWarn {
		t.Errorf("status = %q, want warn", got.Status)
	}
	if !strings.Contains(got.Detail, "old") || !strings.Contains(got.Detail, "new") {
		t.Errorf("detail = %q, want both fingerprints", got.Detail)
	}
}

// Declaring a type the corpus never mentions is untidy, not broken: it costs
// prompt budget and nothing else. Reporting it as a warning would bury the real
// drift under it.
func TestDoctorTreatsAnUnusedDeclaredTypeAsTidyNotBroken(t *testing.T) {
	got := checkVocabularyDrift(&Inventory{Drift: &OntologyDrift{
		Registered: true, Fingerprint: "abc", StoredFingerprint: "abc",
		UnusedNodeTypes: []string{"WANTunnel"},
	}}, nil)

	if got.Status != CheckOK {
		t.Errorf("status = %q, want ok", got.Status)
	}
	if !strings.Contains(got.Detail, "never extracted") {
		t.Errorf("detail = %q, want the unused count still visible", got.Detail)
	}
}

// A code-only knowledge base declares no prose vocabulary and is not broken for
// it — expansion falls back to the built-in code edges, which is right for a
// graph whose edges are calls and imports.
func TestDoctorIsSilentWhenNoVocabularyWasDeclared(t *testing.T) {
	got := checkVocabularyDrift(&Inventory{Drift: &OntologyDrift{}}, nil)
	if got.Status != CheckOK {
		t.Errorf("status = %q, want ok", got.Status)
	}
}

func TestTopTypesNamesTheWorstOffendersAndCountsTheRest(t *testing.T) {
	got := topTypes(map[string]int{"a": 1, "b": 9, "c": 5, "d": 3, "e": 2})
	if want := "b×9, c×5, d×3, +2 more"; got != want {
		t.Errorf("topTypes = %q, want %q", got, want)
	}
}
