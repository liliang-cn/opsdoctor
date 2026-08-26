package knowledge

import (
	"context"
	"strings"
	"testing"

	cortexdb "github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
)

// storageStore is the fixture these walks run over: a pool backing a resource,
// with `backs` declared StoragePool → DRBDResource and `related` declared with
// no ends at all.
func storageStore(t *testing.T) *Store {
	t.Helper()
	s := openStore(t,
		WithOntology("test",
			[]string{"StoragePool", "DRBDResource", "Node"},
			[]string{"backs", "related"}),
		WithRelationTypes([]string{"backs", "related"}),
		WithRelationEnds([]RelationEnd{{
			Name: "backs", From: []string{"StoragePool"}, To: []string{"DRBDResource"},
		}}))
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "tank", Type: "StoragePool"},
		cortexdb.ToolEntityInput{Name: "r0", Type: "DRBDResource"},
		cortexdb.ToolEntityInput{Name: "node-a", Type: "Node"})
	relate(t, s, "tank", "r0", "backs")
	relate(t, s, "node-a", "r0", "related")
	return s
}

// The question retrieval cannot answer: "which pools back this resource" has an
// exact answer sitting in the graph, and asking it through a vector index means
// hoping the right chunk scores well and that its neighbourhood happens to
// contain it.
func TestWalkFollowsADeclaredRelationInwards(t *testing.T) {
	s := storageStore(t)

	got, err := s.Walk(context.Background(), "r0", "backs", WalkIn)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if !got.Typed {
		t.Error("backs declared its ends, so the walk must be the typed one")
	}
	if len(got.Steps) != 1 || got.Steps[0].Name != "tank" {
		t.Fatalf("steps = %+v, want the pool", got.Steps)
	}
	if !strings.Contains(got.Steps[0].Relation, "backs it") {
		t.Errorf("relation = %q, want it to read as the pool's own assertion", got.Steps[0].Relation)
	}
}

// Direction is the whole point: walking `backs` outward from a resource must
// find nothing, because a resource backs nothing.
func TestWalkOutwardsAlongADeclaredRelationRespectsItsDirection(t *testing.T) {
	s := storageStore(t)

	got, err := s.Walk(context.Background(), "r0", "backs", WalkOut)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got.Steps) != 0 {
		t.Errorf("steps = %+v, want none — backs runs the other way", got.Steps)
	}
}

// A relation whose ends the domain left open cannot be walked by link side:
// both sides point at the Entity supertype, nothing is stored under that name,
// and a typed traversal would return nothing — an empty answer that reads
// exactly like "this node has no such neighbours". It falls back to following
// edges, which is the honest behaviour for a vocabulary that said nothing.
func TestWalkFallsBackForARelationWithNoDeclaredEnds(t *testing.T) {
	s := storageStore(t)

	got, err := s.Walk(context.Background(), "r0", "related", WalkBoth)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if got.Typed {
		t.Error("related declared no ends; the walk must not claim to be typed")
	}
	if len(got.Steps) != 1 || got.Steps[0].Name != "node-a" {
		t.Fatalf("steps = %+v, want the node reached by the untyped edge", got.Steps)
	}
	if !strings.Contains(got.Steps[0].Relation, "related it") {
		t.Errorf("relation = %q, want the incoming reading", got.Steps[0].Relation)
	}
}

// With no relation named, every edge in the vocabulary is followed — the
// "what is this connected to at all" question.
func TestWalkWithoutARelationFollowsTheWholeVocabulary(t *testing.T) {
	s := storageStore(t)

	got, err := s.Walk(context.Background(), "r0", "", WalkBoth)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	names := map[string]bool{}
	for _, st := range got.Steps {
		names[st.Name] = true
	}
	if !names["tank"] || !names["node-a"] {
		t.Errorf("steps = %+v, want both neighbours", got.Steps)
	}
}

// A typed walk returns only nodes of the far end's declared type, which is what
// the declaration buys at runtime — and it is a different answer from an
// untyped hop. The misdirected edges drift reports are exactly the ones a typed
// walk leaves out.
func TestATypedWalkExcludesAnEdgeThatContradictsTheDeclaredEnds(t *testing.T) {
	s := storageStore(t)
	// A `backs` from a Node, which the declaration does not admit. Drift
	// reports it; a typed walk must not return it as though it were a pool.
	relate(t, s, "node-a", "r0", "backs")

	got, err := s.Walk(context.Background(), "r0", "backs", WalkIn)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got.Steps) != 1 || got.Steps[0].Name != "tank" {
		t.Fatalf("steps = %+v, want only the declared StoragePool", got.Steps)
	}

	// And the same walk without the ontology's help does return it, which is
	// what makes the exclusion above a property of the declaration rather than
	// of the data.
	untyped, err := s.walkByEdge(context.Background(), []string{got0ID(t, s, "r0")}, "backs", WalkIn)
	if err != nil {
		t.Fatalf("untyped walk: %v", err)
	}
	if len(untyped) != 2 {
		t.Errorf("untyped steps = %+v, want both the pool and the node", untyped)
	}
}

// A name the graph does not know is an answer, not a failure — and the caller
// has to be able to tell it from a lookup that broke.
func TestWalkFromAnUnknownNameAnswersEmpty(t *testing.T) {
	s := storageStore(t)

	got, err := s.Walk(context.Background(), "no-such-thing", "backs", WalkIn)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got.Steps) != 0 {
		t.Errorf("steps = %+v, want none", got.Steps)
	}
}

func TestWalkRejectsAnUnknownDirection(t *testing.T) {
	s := storageStore(t)
	if _, err := s.Walk(context.Background(), "r0", "backs", "sideways"); err == nil {
		t.Error("an unknown direction must be refused rather than quietly meaning both")
	}
}

// got0ID resolves a name to the node id the walk would start from.
func got0ID(t *testing.T, s *Store, name string) string {
	t.Helper()
	found, err := s.tb.FindNodes(context.Background(), cortexdb.ToolFindNodesRequest{Names: []string{name}, Limit: 1})
	if err != nil || len(found.Matches) == 0 || len(found.Matches[0].Nodes) == 0 {
		t.Fatalf("resolve %q: %v", name, err)
	}
	return found.Matches[0].Nodes[0].ID
}

// One thing spelled two ways is one thing.
//
// Entity ids keep separators, so "DRBDResource" lands on entity:drbdresource
// and "DRBD resource" on entity:drbd_resource. Both are the same concept, both
// carry the declared type, and whichever spelling the extractor used is where
// that document's edges attached. Eight pairs had split this way on the SDS
// base.
//
// FindNodes hands back every candidate it ranked, and this walk used to take
// Matches[0].Nodes[0] and drop the rest — so the answer depended on which
// spelling the caller happened to type. The failure is worse than a miss: the
// walk resolves, reports a real match, and says "nothing is connected this
// way", which reads as a finding about the graph rather than an artefact of
// spelling.
func TestWalkPoolsSpellingsOfOneName(t *testing.T) {
	s := storageStore(t)
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "DRBD resource", Type: "DRBDResource"},
		cortexdb.ToolEntityInput{Name: "DRBDResource", Type: "DRBDResource"})
	// The edge attaches to the spaced spelling only.
	relate(t, s, "tank", "DRBD resource", "backs")

	for _, spelling := range []string{"DRBD resource", "DRBDResource", "drbd_resource", "DRBD-Resource"} {
		got, err := s.Walk(context.Background(), spelling, "backs", WalkIn)
		if err != nil {
			t.Fatalf("walk %q: %v", spelling, err)
		}
		if len(got.Steps) != 1 || got.Steps[0].Name != "tank" {
			t.Errorf("walk %q = %+v, want the pool that backs it", spelling, got.Steps)
		}
	}
}

// Pooling is by separator and case only. FindNodes also returns weaker
// candidates it reached by substring, and a plural is a different word:
// collapsing it would take a guess at morphology in every language the material
// is written in. So "DRBD resources" stays its own node — doctor reports the
// pair instead of the walk silently merging it.
func TestWalkDoesNotPoolAPlural(t *testing.T) {
	s := storageStore(t)
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "DRBD resource", Type: "DRBDResource"},
		cortexdb.ToolEntityInput{Name: "DRBD resources", Type: "DRBDResource"})
	relate(t, s, "tank", "DRBD resources", "backs")

	got, err := s.Walk(context.Background(), "DRBD resource", "backs", WalkIn)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got.Steps) != 0 {
		t.Errorf("steps = %+v, want none — a plural is not a spelling", got.Steps)
	}
}

// Reporting a split concept without being able to close it leaves the operator
// holding a warning and no lever. Resolution is cortexdb's, and it merges by
// the same case/separator key a pooled walk uses — but it reads a node's
// content as its name, so it must be told this domain's types or it will merge
// every package's main.go into one file.
func TestResolveSpellingsMergesOnlyTheDomainsOwnEntities(t *testing.T) {
	s := storageStore(t)
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "DRBD resource", Type: "DRBDResource"},
		cortexdb.ToolEntityInput{Name: "DRBDResource", Type: "DRBDResource"},
		cortexdb.ToolEntityInput{ID: "repo:cli/main.go", Name: "main.go", Type: "file"},
		cortexdb.ToolEntityInput{ID: "repo:agent/main.go", Name: "main.go", Type: "file"})
	relate(t, s, "tank", "DRBD resource", "backs")

	report, err := s.ResolveSpellings(context.Background(), false)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if report.Merged != 1 {
		t.Fatalf("merged = %d (%+v), want only the prose pair", report.Merged, report.Groups)
	}

	var files int
	if err := s.db.SQL().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM graph_nodes WHERE node_type = 'file'`).Scan(&files); err != nil {
		t.Fatalf("count files: %v", err)
	}
	if files != 2 {
		t.Errorf("file nodes = %d, want the code graph untouched", files)
	}

	// And the merge is what the walk was papering over: one node now holds the
	// edge, so doctor goes quiet.
	if got := s.checkDuplicateEntities(context.Background()); got.Status != CheckOK {
		t.Errorf("doctor = %q (%s), want it satisfied after the merge", got.Status, got.Detail)
	}
}

// A dry run says what it would do and changes nothing, because merging nodes
// is not reversible and an operator should see the list first.
func TestResolveSpellingsDryRunChangesNothing(t *testing.T) {
	s := storageStore(t)
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "DRBD resource", Type: "DRBDResource"},
		cortexdb.ToolEntityInput{Name: "DRBDResource", Type: "DRBDResource"})

	report, err := s.ResolveSpellings(context.Background(), true)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if report.Merged != 1 || !report.DryRun {
		t.Fatalf("report = %+v, want it to describe the merge without making it", report)
	}
	if got := s.checkDuplicateEntities(context.Background()); got.Status != CheckWarn {
		t.Errorf("doctor = %q, want the pair still there after a dry run", got.Status)
	}
}

// A flag is not a spelling of an identifier, so a walk must not pool them.
//
// The key drops punctuation, which put "--read-only" and "ReadOnly" in one
// pool: a question about the API field would answer with the flag's neighbours
// and the other way round. The CLI's help is ingested precisely so the agent
// gets a flag right, and conflating the two spends that.
func TestWalkKeepsAFlagApartFromAName(t *testing.T) {
	s := storageStore(t)
	upsert(t, s,
		cortexdb.ToolEntityInput{Name: "--read-only", Type: "Node"},
		cortexdb.ToolEntityInput{Name: "ReadOnly", Type: "Node"})
	relate(t, s, "--read-only", "r0", "related")

	got, err := s.Walk(context.Background(), "ReadOnly", "related", WalkOut)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(got.Steps) != 0 {
		t.Errorf("steps = %+v, want none — the flag's edges are not the field's", got.Steps)
	}
}
