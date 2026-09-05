package agents

import (
	"testing"

	"github.com/liliang-cn/opsdoctor/internal/domain"
	"github.com/liliang-cn/opsdoctor/internal/knowledge"
)

// The imported vocabulary the store learns is everything an importer may
// write: the domain's code vocabulary, plus the structural types the two
// importers add on their own (layers and tour steps, tables and foreign keys).
// Those last are not in any domain.toml, because no domain declares them —
// this package does, and a drift report that did not know them reported
// every `layer_contains` edge as the model's invention.
func TestStoreOptionsHandTheStoreEveryImportedType(t *testing.T) {
	dom, err := domain.Load("../../examples/example/domain.toml")
	if err != nil {
		t.Fatalf("load example domain: %v", err)
	}
	s := &knowledge.Store{}
	for _, o := range StoreOptions(dom) {
		o(s)
	}
	nodes, edges := s.ImportedVocabulary()
	for _, want := range []string{"file", "function", "layer", "tour_step", "Table"} {
		if !contains(nodes, want) {
			t.Errorf("imported node types %v lack %q", nodes, want)
		}
	}
	for _, want := range []string{"imports", "layer_contains", "tour_covers", "REFERENCES"} {
		if !contains(edges, want) {
			t.Errorf("imported edge types %v lack %q", edges, want)
		}
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
