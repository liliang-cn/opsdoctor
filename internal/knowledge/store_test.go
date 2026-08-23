package knowledge

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/liliang-cn/cortexdb/v2/pkg/cortexdb"
	"github.com/liliang-cn/cortexdb/v2/pkg/graph"
)

// TestExpandEdgeTypesUsesDeclaredRelationTypes pins the fix: what a domain.toml
// declares is what expansion filters on. Before this, expansion always filtered
// on codeEdgeTypes — a calls/inherits/implements vocabulary no ops corpus emits.
func TestExpandEdgeTypesUsesDeclaredRelationTypes(t *testing.T) {
	s := &Store{}
	WithRelationTypes([]string{"DEPLOYED_ON", "MANAGES"})(s)

	got := s.expandEdgeTypes()
	for _, want := range []string{"DEPLOYED_ON", "MANAGES"} {
		if !hasString(got, want) {
			t.Fatalf("expandEdgeTypes() = %v, missing declared type %q", got, want)
		}
	}
	// The declared vocabulary replaces the fallback rather than joining it: a
	// domain that says what its edges are has said all of it.
	if hasString(got, "inherits") {
		t.Errorf("expandEdgeTypes() = %v, still carries the code fallback", got)
	}
}

// TestExpandEdgeTypesToleratesCasing covers the defence in depth: the filter
// downstream is exact string equality and the edge type in the graph is whatever
// the extracting LLM emitted, so both casings of a declared type are passed.
func TestExpandEdgeTypesToleratesCasing(t *testing.T) {
	s := &Store{}
	WithRelationTypes([]string{"deployed_on"})(s)

	got := s.expandEdgeTypes()
	for _, want := range []string{"deployed_on", "DEPLOYED_ON"} {
		if !hasString(got, want) {
			t.Fatalf("expandEdgeTypes() = %v, missing %q", got, want)
		}
	}
	if len(got) != 2 {
		t.Errorf("expandEdgeTypes() = %v, want exactly the two casings", got)
	}
}

// TestExpandEdgeTypesFallsBackWithoutOntology keeps the no-ontology domain
// behaving exactly as it did before the change.
func TestExpandEdgeTypesFallsBackWithoutOntology(t *testing.T) {
	for name, types := range map[string][]string{
		"nil":   nil,
		"empty": {},
		"blank": {"", "  "},
	} {
		t.Run(name, func(t *testing.T) {
			s := &Store{}
			WithRelationTypes(types)(s)
			got := s.expandEdgeTypes()
			if len(got) != len(codeEdgeTypes) {
				t.Fatalf("expandEdgeTypes() = %v, want the code fallback %v", got, codeEdgeTypes)
			}
			for i, want := range codeEdgeTypes {
				if got[i] != want {
					t.Fatalf("expandEdgeTypes()[%d] = %q, want %q", i, got[i], want)
				}
			}
		})
	}
}

// TestExploreEdgeTypesUnionsDomainVocabulary checks the explorer widens rather
// than replaces: browsing must still reach the imported code and schema edges.
func TestExploreEdgeTypesUnionsDomainVocabulary(t *testing.T) {
	s := &Store{}
	WithRelationTypes([]string{"PROMOTES"})(s)

	got := s.exploreEdgeTypes()
	for _, want := range []string{"PROMOTES", "REFERENCES", "calls"} {
		if !hasString(got, want) {
			t.Fatalf("exploreEdgeTypes() = %v, missing %q", got, want)
		}
	}
}

// TestDeclaredRelationTypesTraverseRealEdges is the end-to-end version of the
// bug: a graph built from a domain's own relations, walked with that domain's
// own vocabulary. It writes the edge the way ingest does and expands it the way
// SearchGraph does, so it fails against the hardcoded whitelist for the reason
// production did — 0 of 415 edges matched.
func TestDeclaredRelationTypesTraverseRealEdges(t *testing.T) {
	ctx := context.Background()
	db, err := cortexdb.Open(cortexdb.DefaultConfig(filepath.Join(t.TempDir(), "k.db")))
	if err != nil {
		t.Fatalf("open cortexdb: %v", err)
	}
	defer db.Close()
	s := &Store{db: db, tb: db.GraphRAGTools()}
	WithRelationTypes([]string{"deployed_on"})(s)

	const doc = "runbook.md"
	if _, err := s.tb.UpsertEntities(ctx, cortexdb.ToolUpsertEntitiesRequest{
		DocumentID: doc,
		Entities: []cortexdb.ToolEntityInput{
			{Name: "Resource", Type: "Resource"},
			{Name: "Node", Type: "Node"},
		},
	}); err != nil {
		t.Fatalf("upsert entities: %v", err)
	}
	// The declaration is lower_snake and the extractor emitted UPPER_SNAKE —
	// the drift that made a case-sensitive filter match nothing.
	if _, err := s.tb.UpsertRelations(ctx, cortexdb.ToolUpsertRelationsRequest{
		DocumentID: doc,
		Relations:  []cortexdb.ToolRelationInput{{From: "Resource", To: "Node", Type: "DEPLOYED_ON"}},
	}); err != nil {
		t.Fatalf("upsert relations: %v", err)
	}

	seed := cortexdb.EntityNodeID("Resource")
	neighbors := func(edgeTypes []string) []string {
		t.Helper()
		exp, err := s.tb.ExpandGraph(ctx, cortexdb.ToolExpandGraphRequest{
			NodeIDs: []string{seed}, MaxHops: 1, EdgeTypes: edgeTypes, Limit: graphNeighborsPerSeed,
		})
		if err != nil {
			t.Fatalf("expand graph: %v", err)
		}
		var out []string
		for _, n := range exp.Nodes {
			if n != nil && n.ID != seed {
				out = append(out, n.ID)
			}
		}
		return out
	}

	if got := neighbors(codeEdgeTypes); len(got) != 0 {
		t.Fatalf("code fallback reached %v; the test no longer reproduces the bug", got)
	}
	if got := neighbors(s.expandEdgeTypes()); len(got) != 1 {
		t.Fatalf("declared vocabulary reached %v, want the one DEPLOYED_ON neighbour", got)
	}
}

func hasString(in []string, want string) bool {
	for _, s := range in {
		if s == want {
			return true
		}
	}
	return false
}

// A code graph imported next to the prose outnumbers it, and expansion returns
// neighbours in the graph's own order. Twelve slots handed out in that order go
// to whatever the importer happened to write first — so an operator asking about
// a gateway got three `function` nodes and the StoragePool that answers the
// question queued behind them.
//
// The domain's own vocabulary is the tiebreak. Types it never declared come from
// the importer or from a model inventing one, and they go second.
func TestDeclaredNeighborTypesAreOfferedFirst(t *testing.T) {
	s := &Store{}
	WithOntology("test", []string{"StoragePool", "Gateway"}, nil)(s)

	got := s.pickNeighbors([]*graph.GraphNode{
		{ID: "f1", NodeType: "function", Content: "mountVolume"},
		{ID: "f2", NodeType: "function", Content: "exportNFS"},
		{ID: "p1", NodeType: "StoragePool", Content: "tank"},
	}, nil, nil)

	if len(got) != 3 {
		t.Fatalf("neighbours = %v, want all three — undeclared types stay reachable, just behind", got)
	}
	if got[0].Type != "StoragePool" {
		t.Errorf("neighbours = %v, want the declared StoragePool first", got)
	}
}

// With nothing declared there is nothing to prefer, and the order must be the
// graph's — the behaviour every domain had before the vocabulary was consulted.
func TestNeighborOrderIsUnchangedWithoutADeclaredVocabulary(t *testing.T) {
	s := &Store{}

	got := s.pickNeighbors([]*graph.GraphNode{
		{ID: "f1", NodeType: "function", Content: "mountVolume"},
		{ID: "p1", NodeType: "StoragePool", Content: "tank"},
	}, nil, nil)

	if len(got) != 2 || got[0].ID != "f1" {
		t.Errorf("neighbours = %v, want the graph's own order", got)
	}
}

// The per-type cap predates the declared/undeclared split and must survive it:
// one dense type filling the budget is the failure it was written for.
func TestOneDeclaredTypeStillCannotTakeEveryNeighborSlot(t *testing.T) {
	s := &Store{}
	WithOntology("test", []string{"VIP", "Gateway"}, nil)(s)

	nodes := make([]*graph.GraphNode, 0, 7)
	for i := range 6 {
		nodes = append(nodes, &graph.GraphNode{ID: fmt.Sprintf("v%d", i), NodeType: "VIP"})
	}
	nodes = append(nodes, &graph.GraphNode{ID: "g1", NodeType: "Gateway"})

	got := s.pickNeighbors(nodes, nil, nil)

	vips := 0
	for _, n := range got {
		if n.Type == "VIP" {
			vips++
		}
	}
	if vips != maxNeighborsPerType {
		t.Errorf("%d VIPs of %d neighbours, want at most %d", vips, len(got), maxNeighborsPerType)
	}
	if len(got) != maxNeighborsPerType+1 {
		t.Errorf("neighbours = %v, want the Gateway to have kept its slot", got)
	}
}
