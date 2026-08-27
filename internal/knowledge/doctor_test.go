package knowledge

import (
	"context"
	"strings"
	"testing"

	cortexdb "github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
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

// A relation pointing the wrong way outranks an invented type. An invented type
// is inert — stored, not walked, visible as an absence. A backwards edge IS
// walked, and produces an answer that states the opposite of what the source
// said, fluently.
func TestDoctorRanksABackwardsRelationAboveAnInventedType(t *testing.T) {
	got := checkVocabularyDrift(&Inventory{Drift: &OntologyDrift{
		Registered: true, Fingerprint: "abc", StoredFingerprint: "abc",
		UndeclaredNodeTypes: map[string]int{"StorageClass": 3},
		MisdirectedEdges: []MisdirectedEdge{{
			Relation: "backs", Want: "StoragePool → DRBDResource",
			Got: "DRBDResource → StoragePool", Count: 14, Reversed: 14,
		}},
	}}, nil)

	if got.Status != CheckWarn {
		t.Errorf("status = %q, want warn", got.Status)
	}
	if !strings.Contains(got.Detail, "backs") {
		t.Errorf("detail = %q, want the misdirected relation, not the invented type", got.Detail)
	}
}

// Every edge of a relation being exactly reversed is one wrong line in
// domain.toml, not the model erring. Telling those apart is the difference
// between an edit and a re-ingest.
func TestDoctorSaysWhenAWholeRelationIsReversed(t *testing.T) {
	whole := checkVocabularyDrift(&Inventory{Drift: &OntologyDrift{
		Registered: true, Fingerprint: "a", StoredFingerprint: "a",
		MisdirectedEdges: []MisdirectedEdge{{Relation: "backs", Count: 14, Reversed: 14}},
	}}, nil)
	if !strings.Contains(whole.Hint, "domain.toml") {
		t.Errorf("hint = %q, want it to point at the declaration", whole.Hint)
	}

	some := checkVocabularyDrift(&Inventory{Drift: &OntologyDrift{
		Registered: true, Fingerprint: "a", StoredFingerprint: "a",
		MisdirectedEdges: []MisdirectedEdge{{Relation: "backs", Count: 14, Reversed: 2}},
	}}, nil)
	if !strings.Contains(some.Hint, "Re-ingest") {
		t.Errorf("hint = %q, want it to point at the sources", some.Hint)
	}
}

// One concept stored under two spellings is invisible to every other check.
//
// Entity ids keep separators, so "DRBDResource" and "DRBD resource" are two
// nodes. Both carry the declared type, so drift sees nothing wrong; both have
// edges, so the connectivity check sees nothing wrong; and the graph reports
// two entities where the material describes one. What splits is the edges —
// each document's attach to whichever spelling it used. A walk pools them now,
// but the pair is still worth naming: it is the extractor being inconsistent,
// and re-ingesting is what actually fixes it.
func TestDoctorReportsOneThingSpelledTwoWays(t *testing.T) {
	s := storageStore(t)
	upsert(t, s,
		toolEntity("DRBD resource", "DRBDResource"),
		toolEntity("DRBDResource", "DRBDResource"),
		toolEntity("thin pool", "StoragePool"),
		toolEntity("thinpool", "StoragePool"))

	got := s.checkDuplicateEntities(context.Background())

	if got.Status != CheckWarn {
		t.Fatalf("status = %q, want warn", got.Status)
	}
	if !strings.Contains(got.Detail, "2 ") {
		t.Errorf("detail = %q, want it to count the pairs", got.Detail)
	}
	if !strings.Contains(got.Detail, "DRBD") {
		t.Errorf("detail = %q, want it to name an example", got.Detail)
	}
}

// A plural is a different word and is not reported: the check would then fire
// on every vocabulary that names both a thing and a set of them, and an
// operator who cannot act on a warning learns to ignore all of them.
func TestDoctorDoesNotCallAPluralADuplicate(t *testing.T) {
	s := storageStore(t)
	upsert(t, s,
		toolEntity("DRBD resource", "DRBDResource"),
		toolEntity("DRBD resources", "DRBDResource"))

	if got := s.checkDuplicateEntities(context.Background()); got.Status != CheckOK {
		t.Errorf("status = %q (%s), want ok", got.Status, got.Detail)
	}
}

// The code graph names one symbol per package, so main.go in seven packages is
// seven files rather than seven spellings. Only nodes typed by the prose
// vocabulary are compared.
func TestDoctorDoesNotCallTwoFilesOfOneNameADuplicate(t *testing.T) {
	s := storageStore(t)
	upsert(t, s,
		cortexdb.ToolEntityInput{ID: "file:a/main.go", Name: "main.go", Type: "file"},
		cortexdb.ToolEntityInput{ID: "file:b/main.go", Name: "main.go", Type: "file"})

	if got := s.checkDuplicateEntities(context.Background()); got.Status != CheckOK {
		t.Errorf("status = %q (%s), want ok", got.Status, got.Detail)
	}
}

func toolEntity(name, typ string) cortexdb.ToolEntityInput {
	return cortexdb.ToolEntityInput{Name: name, Type: typ}
}

// The count and the shape in one sentence have to be about the same edges.
//
// The detail read "%d edges are %s" with the relation's total count and its
// commonest wrong shape, so a relation wrong in several ways reported all of
// them as the most popular one. On the live SDS base it said "20 edges are
// ConfigParameter → ConfigParameter" when twelve were; the other eight were
// four different shapes. An operator who goes looking for twenty finds twelve
// and stops trusting the number.
func TestDoctorDoesNotAttributeEveryWrongEdgeToTheCommonestShape(t *testing.T) {
	got := checkVocabularyDrift(&Inventory{Drift: &OntologyDrift{
		Registered: true, Fingerprint: "abc", StoredFingerprint: "abc",
		MisdirectedEdges: []MisdirectedEdge{{
			Relation: "configured_by", Want: "Node → ConfigParameter",
			Got: "ConfigParameter → ConfigParameter", Count: 20, GotCount: 12,
		}},
	}}, nil)

	if strings.Contains(got.Detail, "20 edges are ConfigParameter") {
		t.Errorf("detail claims every wrong edge has the commonest shape:\n%s", got.Detail)
	}
	for _, want := range []string{"20", "12"} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail = %q, want it to carry %s", got.Detail, want)
		}
	}
}

// When every wrong edge really does have one shape, say so plainly rather than
// making the reader compare two equal numbers.
func TestDoctorSaysPlainlyWhenOneShapeIsAllOfThem(t *testing.T) {
	got := checkVocabularyDrift(&Inventory{Drift: &OntologyDrift{
		Registered: true, Fingerprint: "abc", StoredFingerprint: "abc",
		MisdirectedEdges: []MisdirectedEdge{{
			Relation: "notifies", Want: "Event → NotificationChannel",
			Got: "NotificationChannel → Event", Count: 7, GotCount: 7, Reversed: 7,
		}},
	}}, nil)

	if !strings.Contains(got.Detail, "7 edges are NotificationChannel → Event") {
		t.Errorf("detail = %q, want the plain form when the shape covers them all", got.Detail)
	}
}

// Misdirected edges cluster on a few nodes, and saying which turns a list of
// edge complaints into a short list of things to look at.
//
// Of eighteen non-conforming edges on the live SDS base, six came from two
// nodes: `service-ip`, which the source calls a flag and the graph typed a
// CLICommand, and `OCF agents`, correctly typed and simply not a configuration
// parameter. The two need opposite fixes — one is a mistyped node, the other a
// misused relation — so this names the concentration and stops there. Proposing
// the retype would have been wrong half the time.
func TestDoctorNamesTheNodesThatMostMisdirectedEdgesRunThrough(t *testing.T) {
	got := checkVocabularyDrift(&Inventory{Drift: &OntologyDrift{
		Registered: true, Fingerprint: "abc", StoredFingerprint: "abc",
		MisdirectedEdges: []MisdirectedEdge{{
			Relation: "configured_by", Want: "Gateway → ConfigParameter",
			Got: "Gateway → CLICommand", Count: 18, GotCount: 3,
		}},
		MisdirectedNodes: []MisdirectedNode{
			{Name: "service-ip", Type: "CLICommand", Edges: 3},
			{Name: "OCF agents", Type: "OCFAgent", Edges: 3},
		},
	}}, nil)

	if !strings.Contains(got.Hint, "service-ip") || !strings.Contains(got.Hint, "OCF agents") {
		t.Errorf("hint does not name the nodes the edges run through:\n%s", got.Hint)
	}
	if !strings.Contains(got.Hint, "6 of") {
		t.Errorf("hint = %q, want it to say how much of the total they carry", got.Hint)
	}
}

// With the edges spread evenly there is no concentration to point at, and a
// list of one-edge nodes is the same list of complaints in another shape.
func TestDoctorDoesNotListNodesCarryingOneEdgeEach(t *testing.T) {
	got := checkVocabularyDrift(&Inventory{Drift: &OntologyDrift{
		Registered: true, Fingerprint: "abc", StoredFingerprint: "abc",
		MisdirectedEdges: []MisdirectedEdge{{
			Relation: "configured_by", Want: "a", Got: "b", Count: 4, GotCount: 1,
		}},
		MisdirectedNodes: []MisdirectedNode{{Name: "one", Type: "T", Edges: 1}},
	}}, nil)

	if strings.Contains(got.Hint, "one") && strings.Contains(got.Hint, "1 of") {
		t.Errorf("hint lists a node carrying a single edge:\n%s", got.Hint)
	}
}
