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
	untyped, err := s.walkByEdge(context.Background(), got0ID(t, s, "r0"), "backs", WalkIn)
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
